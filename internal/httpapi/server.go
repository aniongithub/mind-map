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
type Server struct {
	deps Deps

	mu         sync.Mutex
	sync       *mindsync.Manager // nil when sync disabled or stopped
	syncCancel context.CancelFunc
}

// New constructs the HTTP handler. It starts the sync manager if the
// initial config has sync enabled; the returned handler must be served
// by an *http.Server. Callers should close Deps.StopCh to trigger
// shutdown of background goroutines started by the server (e.g. sync).
func New(d Deps) http.Handler {
	if d.GetVersion == nil {
		d.GetVersion = func() string { return "" }
	}
	s := &Server{deps: d}

	if d.Cfg != nil && d.Cfg.Sync.Enabled && len(d.Cfg.Sync.Remotes()) > 0 {
		s.startSync(context.Background())
	}

	// Stop sync when the server is told to stop.
	if d.StopCh != nil {
		go func() {
			<-d.StopCh
			s.stopSync()
		}()
	}

	mux := http.NewServeMux()
	s.register(mux)
	return logging.RecoverMiddleware(logging.RequestMiddleware(mux))
}

// register wires every endpoint. Handler methods live on *Server so they
// can read and mutate state under s.mu.
func (s *Server) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/version", s.getVersion)
	mux.HandleFunc("GET /api/context", s.getContext)
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
	mux.HandleFunc("GET /api/sync/status", s.getSyncStatus)
	mux.Handle("/", s.staticHandler())
}

// ---------------------------------------------------------------------
// Sync supervisor
// ---------------------------------------------------------------------

// startSync creates a new sync.Manager from the current config and starts
// it. Caller must NOT hold s.mu (we take it here).
func (s *Server) startSync(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startSyncLocked(ctx)
}

func (s *Server) startSyncLocked(ctx context.Context) {
	if s.sync != nil {
		return
	}
	mgr := mindsync.NewManager(s.deps.Wiki.Root(), s.deps.CfgPath, s.deps.Cfg, s.deps.Wiki)
	syncCtx, cancel := context.WithCancel(ctx)
	if err := mgr.Start(syncCtx); err != nil {
		cancel()
		slog.Error("failed to start sync", slog.Any("error", err))
		return
	}
	s.sync = mgr
	s.syncCancel = cancel
}

// stopSync stops the running sync manager, if any. Safe to call when
// sync is already stopped.
func (s *Server) stopSync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopSyncLocked()
}

func (s *Server) stopSyncLocked() {
	if s.sync == nil {
		return
	}
	s.sync.Stop()
	if s.syncCancel != nil {
		s.syncCancel()
	}
	s.sync = nil
	s.syncCancel = nil
}

// applyConfig reconciles the running sync manager with a new config.
// - disabled -> enabled: start a fresh manager
// - enabled -> disabled: stop the manager
// - both enabled, interval unchanged: hot-reload mappings
// - both enabled, interval changed: stop + start to pick up the new ticker
// Caller must hold s.mu.
func (s *Server) applyConfigLocked(newCfg *config.Config) {
	s.deps.Cfg = newCfg

	wantSync := newCfg.Sync.Enabled && len(newCfg.Sync.Remotes()) > 0
	switch {
	case !wantSync && s.sync != nil:
		s.stopSyncLocked()
	case wantSync && s.sync == nil:
		s.startSyncLocked(context.Background())
	case wantSync && s.sync != nil:
		if s.sync.Interval() != newCfg.Sync.ParseInterval() {
			// Interval is captured at Start time; need a restart.
			s.stopSyncLocked()
			s.startSyncLocked(context.Background())
		} else if err := s.sync.Reload(newCfg); err != nil {
			slog.Error("sync reload failed", slog.Any("error", err))
		}
	}
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
	s.mu.Lock()
	s.applyConfigLocked(&incoming)
	s.mu.Unlock()

	slog.Info("settings saved and applied", slog.String("path", s.deps.CfgPath))
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

// writeJSON encodes v as application/json. Errors are logged but not
// surfaced to the client (the connection is usually already half-written
// by the time encoding fails on something).
func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(v); err != nil {
		slog.Debug("response encode failed", slog.Any("error", err))
	}
}
