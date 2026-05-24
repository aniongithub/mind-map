// Package httpapi implements the REST API and static web UI handlers
// for the mind-map HTTP server. It is the only package that knows about
// HTTP; the wiki engine, sync, and config are pure data layers.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
	"github.com/aniongithub/mind-map/internal/logging"
	mindsync "github.com/aniongithub/mind-map/internal/sync"
	"github.com/aniongithub/mind-map/internal/wiki"
)

// Deps bundles the runtime objects the HTTP server needs. None of the
// concrete dependencies (Wiki, sync.Manager, config) know anything about
// HTTP; this struct is the seam.
type Deps struct {
	// Wiki is the storage engine. Required.
	Wiki *wiki.Wiki

	// CfgPath is the absolute path to config.json (for read/write).
	CfgPath string

	// Cfg is the initial config. The server takes ownership and mutates
	// it on settings PUT.
	Cfg *config.Config

	// GetVersion returns the build version string. Called per /api/version
	// request so callers can swap implementations in tests.
	GetVersion func() string

	// StopCh is closed by /api/restart to signal a graceful shutdown.
	// The main package is responsible for re-execing.
	StopCh chan struct{}

	// WebFS is the embedded or override filesystem for the SPA.
	// May be nil; in that case a placeholder page is served.
	WebFS fs.FS
}

// Server holds the live handler state, including the sync supervisor.
// It is not exported beyond New(); the only public surface is the
// http.Handler returned from New.
//
// Concurrency model:
//   - rootCtx is created in New() and cancelled exactly once when the
//     server is told to stop (StopCh closed). All sync manager contexts
//     are derived from it, so an HTTP-level shutdown propagates into
//     in-flight git operations.
//   - s.mu protects s.deps.Cfg, s.sync, s.syncCancel, and shuttingDown.
//     The lock is held only across in-memory mutations; potentially
//     slow side effects (mgr.Start, mgr.Stop, git I/O) happen outside.
//   - actionMu serializes all calls into sync.Manager.Start and
//     sync.Manager.Stop. The sync package writes m.cancel/m.done in
//     Start without internal locking, so concurrent Start+Stop on the
//     same manager would be a race. actionMu makes those calls
//     strictly sequential across applyConfig and shutdown.
//   - shuttingDown is set under s.mu; applyConfig checks it under s.mu
//     and skips its action if shutdown has begun.
type Server struct {
	deps Deps

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// actionMu serializes Start/Stop calls on any sync.Manager owned by
	// this Server. Held outside s.mu. See concurrency model above.
	actionMu sync.Mutex

	mu           sync.Mutex
	sync         *mindsync.Manager // nil when sync disabled or stopped
	syncCancel   context.CancelFunc
	shuttingDown bool
}

// New constructs the HTTP handler. It starts the sync manager if the
// initial config has sync enabled; the returned handler must be served
// by an *http.Server. Callers should close Deps.StopCh to trigger
// shutdown of background goroutines started by the server (e.g. sync).
func New(d Deps) http.Handler {
	if d.GetVersion == nil {
		d.GetVersion = func() string { return "" }
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{deps: d, rootCtx: ctx, rootCancel: cancel}

	if d.Cfg != nil && d.Cfg.Sync.Enabled && len(d.Cfg.Sync.Remotes()) > 0 {
		// Start outside any lock — the supervisor lock doesn't exist yet,
		// and Start may block on initial sync. Hold actionMu to satisfy
		// the contract that Start/Stop are never concurrent on the same
		// manager (matters once a StopCh listener is wired below).
		mgr, syncCtx, syncCancel := s.newSyncManager()
		s.actionMu.Lock()
		err := mgr.Start(syncCtx)
		s.actionMu.Unlock()
		if err != nil {
			syncCancel()
			slog.Error("failed to start sync", slog.Any("error", err))
		} else {
			s.mu.Lock()
			s.sync = mgr
			s.syncCancel = syncCancel
			s.mu.Unlock()
		}
	}

	// Stop sync when the server is told to stop. Cancel rootCtx first
	// so any in-flight git operations get a chance to exit cleanly
	// before mgr.Stop() waits for the loop to drain.
	if d.StopCh != nil {
		go func() {
			<-d.StopCh
			s.shutdown()
		}()
	}

	mux := http.NewServeMux()
	s.register(mux)
	return logging.RecoverMiddleware(logging.RequestMiddleware(mux))
}

// shutdown is called exactly once when StopCh closes. It cancels the
// root context (signalling all derived sync contexts), then stops the
// running sync manager. Subsequent applyConfig calls become no-ops.
// Holds actionMu around mgr.Stop to serialize with any in-flight
// applyConfig.act.run that may be starting a manager.
func (s *Server) shutdown() {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.shuttingDown = true
	mgr := s.sync
	s.sync = nil
	s.syncCancel = nil
	s.mu.Unlock()

	// Cancel root first so git operations in-flight see the cancellation.
	s.rootCancel()
	if mgr != nil {
		s.actionMu.Lock()
		mgr.Stop()
		s.actionMu.Unlock()
	}
}

// register wires every endpoint. Handler methods live on *Server so they
// can read and mutate state under s.mu.
func (s *Server) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/version", s.getVersion)
	mux.HandleFunc("GET /api/context", s.getContext)
	mux.HandleFunc("GET /api/digest", s.getDigest)
	mux.HandleFunc("GET /api/pages", s.listPages)
	mux.HandleFunc("GET /api/pages/{path...}", s.getPage)
	mux.HandleFunc("POST /api/pages", s.createPage)
	mux.HandleFunc("PUT /api/pages/{path...}", s.updatePage)
	mux.HandleFunc("DELETE /api/pages/{path...}", s.deletePage)
	mux.HandleFunc("GET /api/search", s.searchPages)
	mux.HandleFunc("GET /api/backlinks/{path...}", s.getBacklinks)
	mux.HandleFunc("GET /api/links", s.allLinks)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/settings/path", s.getSettingsPath)
	mux.HandleFunc("POST /api/restart", s.postRestart)
	mux.HandleFunc("POST /api/reindex", s.postReindex)
	mux.HandleFunc("GET /api/sync/status", s.getSyncStatus)
	mux.Handle("/", s.staticHandler())
}

// ---------------------------------------------------------------------
// Sync supervisor
// ---------------------------------------------------------------------
//
// The supervisor follows a strict lock discipline: s.mu is held only
// while inspecting/mutating in-memory pointers. Any blocking call into
// sync.Manager (Start, Stop, Status) happens outside s.mu. Callers
// computing a transition while holding the lock build a small action
// struct and then execute it after Unlock.

// newSyncManager constructs a sync.Manager plus a child context derived
// from rootCtx. The returned context is what Start should be passed; the
// returned cancel cancels just that child (independent of rootCtx).
func (s *Server) newSyncManager() (*mindsync.Manager, context.Context, context.CancelFunc) {
	syncCtx, cancel := context.WithCancel(s.rootCtx)
	mgr := mindsync.NewManager(s.deps.Wiki.Root(), s.deps.CfgPath, s.deps.Cfg, s.deps.Wiki)
	return mgr, syncCtx, cancel
}

// syncAction describes the side effect applyConfig wants performed
// after releasing s.mu. Exactly one of stop/start (or both) may be set.
type syncAction struct {
	stopOld  *mindsync.Manager
	stopOldC context.CancelFunc
	startNew *mindsync.Manager
	startCtx context.Context // context to pass to startNew.Start
}

// runAction executes a syncAction, serializing Start/Stop calls under
// actionMu so they can't race with shutdown() or each other.
func (s *Server) runAction(a syncAction) {
	// Stop first so the new manager doesn't race the old one on the
	// shared shadow clone directory.
	if a.stopOld != nil {
		s.actionMu.Lock()
		a.stopOld.Stop()
		s.actionMu.Unlock()
		if a.stopOldC != nil {
			a.stopOldC()
		}
	}
	if a.startNew != nil {
		s.actionMu.Lock()
		// If shutdown ran while we were waiting, skip the start entirely
		// — the manager's context is already cancelled and shutdown has
		// declared we're tearing down. Without this check we'd briefly
		// start a goroutine that exits on its first select, which is
		// harmless but wastes a MkdirAll and clutters the logs.
		s.mu.Lock()
		down := s.shuttingDown
		s.mu.Unlock()
		if !down {
			if err := a.startNew.Start(a.startCtx); err != nil {
				slog.Error("failed to start sync", slog.Any("error", err))
			}
		}
		s.actionMu.Unlock()
	}
}

// applyConfig reconciles the running sync manager with a new config.
//   - disabled -> enabled: start a fresh manager
//   - enabled -> disabled: stop the manager
//   - both enabled, interval unchanged: hot-reload mappings in place
//   - both enabled, interval changed: stop + start (ticker is captured
//     at Start time and can't be retuned in place)
//
// Mutations to s.deps.Cfg, s.sync, s.syncCancel happen under s.mu;
// blocking Start/Stop calls happen after Unlock via runAction, which
// uses actionMu to serialize with shutdown().
func (s *Server) applyConfig(newCfg *config.Config) {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		slog.Warn("settings change applied during shutdown; sync not reconfigured")
		return
	}
	s.deps.Cfg = newCfg

	wantSync := newCfg.Sync.Enabled && len(newCfg.Sync.Remotes()) > 0
	var act syncAction

	switch {
	case !wantSync && s.sync != nil:
		// enabled -> disabled
		act.stopOld, act.stopOldC = s.sync, s.syncCancel
		s.sync, s.syncCancel = nil, nil

	case wantSync && s.sync == nil:
		// disabled -> enabled
		mgr, syncCtx, cancel := s.newSyncManager()
		s.sync, s.syncCancel = mgr, cancel
		act.startNew, act.startCtx = mgr, syncCtx

	case wantSync && s.sync != nil:
		if s.sync.Interval() != newCfg.Sync.ParseInterval() {
			// Interval changed — full restart needed.
			act.stopOld, act.stopOldC = s.sync, s.syncCancel
			mgr, syncCtx, cancel := s.newSyncManager()
			s.sync, s.syncCancel = mgr, cancel
			act.startNew, act.startCtx = mgr, syncCtx
		} else if err := s.sync.Reload(newCfg); err != nil {
			// Reload is cheap and in-memory; safe under lock.
			slog.Error("sync reload failed", slog.Any("error", err))
		}
	}
	s.mu.Unlock()

	s.runAction(act)
}

// ---------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------

func (s *Server) getVersion(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, map[string]string{"version": s.deps.GetVersion()})
}

func (s *Server) getContext(rw http.ResponseWriter, r *http.Request) {
	wctx, err := s.deps.Wiki.Context(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, wctx)
}

// getDigest handles GET /api/digest. Returns the full Digest struct
// (page count, cloud terms, recents LRU, per-area summaries, rendered
// markdown). Intended for two callers:
//
//   - Agents / MCP clients that prefer the HTTP path over the MCP
//     tool (e.g. tests, scripts, or alternate clients).
//   - The WebUI, which can render its own widgets (e.g. a word-cloud
//     visualization) off the structured fields rather than parsing
//     the markdown.
//
// Cheap on cache hit, sub-millisecond on miss. Safe to call frequently
// (e.g. WebUI polling); the in-memory digestCache absorbs the load.
func (s *Server) getDigest(rw http.ResponseWriter, r *http.Request) {
	d, err := s.deps.Wiki.Digest(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, d)
}

func (s *Server) listPages(rw http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	pages, err := s.deps.Wiki.ListPages(r.Context(), prefix)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, pages)
}

func (s *Server) getPage(rw http.ResponseWriter, r *http.Request) {
	page, err := s.deps.Wiki.GetPage(r.Context(), r.PathValue("path"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(rw, page)
}

func (s *Server) createPage(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" || req.Content == "" {
		http.Error(rw, "path and content are required", http.StatusBadRequest)
		return
	}
	if err := s.deps.Wiki.CreatePage(r.Context(), req.Path, req.Content); err != nil {
		http.Error(rw, err.Error(), http.StatusConflict)
		return
	}
	rw.WriteHeader(http.StatusCreated)
	writeJSON(rw, map[string]string{"status": "created", "path": req.Path})
}

func (s *Server) updatePage(rw http.ResponseWriter, r *http.Request) {
	pagePath := r.PathValue("path")
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.deps.Wiki.UpdatePage(r.Context(), pagePath, req.Content); err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(rw, map[string]string{"status": "updated", "path": pagePath})
}

func (s *Server) deletePage(rw http.ResponseWriter, r *http.Request) {
	pagePath := r.PathValue("path")
	if err := s.deps.Wiki.DeletePage(r.Context(), pagePath); err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(rw, map[string]string{"status": "deleted", "path": pagePath})
}

func (s *Server) searchPages(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(rw, "q parameter is required", http.StatusBadRequest)
		return
	}
	results, err := s.deps.Wiki.Search(r.Context(), q, 20)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, results)
}

func (s *Server) getBacklinks(rw http.ResponseWriter, r *http.Request) {
	backlinks, err := s.deps.Wiki.GetBacklinks(r.Context(), r.PathValue("path"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, backlinks)
}

func (s *Server) allLinks(rw http.ResponseWriter, r *http.Request) {
	links, err := s.deps.Wiki.AllLinks(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, links)
}

func (s *Server) getSettings(rw http.ResponseWriter, r *http.Request) {
	current, err := config.Load(s.deps.CfgPath)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, current)
}

func (s *Server) putSettings(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(rw, "failed to read body", http.StatusBadRequest)
		return
	}

	var incoming config.Config
	if err := json.Unmarshal(body, &incoming); err != nil {
		http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if incoming.Sync.Enabled && incoming.Sync.Default == "" && len(incoming.Sync.Mappings) == 0 {
		http.Error(rw, "sync requires at least a default remote or one mapping", http.StatusBadRequest)
		return
	}

	if err := config.Save(s.deps.CfgPath, &incoming); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	// Hot-reload the running sync supervisor. This replaces the previous
	// behaviour where settings only took effect after /api/restart.
	// applyConfig handles its own locking; potentially slow Start/Stop
	// calls happen outside the lock so other handlers stay responsive.
	// During shutdown, applyConfig logs and returns; the config file is
	// already on disk so the next start will pick up the user's intent.
	s.applyConfig(&incoming)

	slog.Info("settings saved", slog.String("path", s.deps.CfgPath))
	writeJSON(rw, &incoming)
}

func (s *Server) getSettingsPath(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, map[string]string{"path": s.deps.CfgPath})
}

func (s *Server) postRestart(rw http.ResponseWriter, r *http.Request) {
	slog.Info("restart requested via API")
	writeJSON(rw, map[string]string{"status": "restarting"})

	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}

	stopCh := s.deps.StopCh
	if stopCh == nil {
		slog.Error("restart requested but no stop channel configured")
		return
	}

	logging.SafeGo("restart", func() {
		// Give the response time to reach the client.
		time.Sleep(500 * time.Millisecond)

		close(stopCh)
		time.Sleep(500 * time.Millisecond)

		exe, err := os.Executable()
		if err != nil {
			slog.Error("restart failed: cannot find executable", slog.Any("error", err))
			return
		}
		slog.Info("restarting", slog.String("exe", exe))
		_ = syscall.Exec(exe, os.Args, os.Environ())
	})
}

func (s *Server) getSyncStatus(rw http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	mgr := s.sync
	s.mu.Unlock()

	if mgr != nil {
		writeJSON(rw, mgr.Status())
		return
	}
	writeJSON(rw, mindsync.Status{Enabled: false})
}

// postReindex handles POST /api/reindex. Triggers a full reindex pass
// against the on-disk wiki and returns the resulting stats.
//
// Safe to call repeatedly and safe to call concurrently with the sync
// loop — wiki.Reindex acquires per-page locks rather than holding a
// global lock, so requests don't stall the server.
func (s *Server) postReindex(rw http.ResponseWriter, r *http.Request) {
	stats, err := s.deps.Wiki.Reindex(r.Context())
	if err != nil {
		http.Error(rw, "reindex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, stats)
}

func (s *Server) staticHandler() http.Handler {
	if s.deps.WebFS != nil {
		return http.FileServerFS(s.deps.WebFS)
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/html")
		fmt.Fprint(rw, `<!DOCTYPE html><html><body style="font-family:sans-serif;padding:40px">
				<h1>mind-map</h1><p>WebUI not built. Run <code>npm run build</code> in <code>webui/</code></p>
			</body></html>`)
	})
}

// writeJSON encodes v as application/json. If encoding fails we can't
// recover (headers are already sent), but we log at Warn so operators
// see persistent serialization bugs. Common causes: nil channels in
// structs, unexported fields that need a marshaler, or cyclic graphs.
func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(v); err != nil {
		slog.Warn("response encode failed", slog.Any("error", err))
	}
}
