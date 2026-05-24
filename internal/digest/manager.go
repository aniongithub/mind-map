// Package digest runs the background maintenance for a wiki's per-
// conversation orientation digest: a periodic rebuild of the word/
// phrase cloud and a periodic flush of the active-use recents LRU
// to SQLite.
//
// The package mirrors internal/sync in shape: a Manager constructed
// over a *wiki.Wiki, with Start(ctx) / Stop() lifecycle that the
// embedder (cmd/mind-map, internal/httpapi) supervises. Keeping the
// tickers out of the Wiki itself preserves the same separation sync
// already established — the storage engine has no goroutines of its
// own; lifecycle is the embedder's concern.
package digest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aniongithub/mind-map/internal/wiki"
)

// Default tick intervals match the plan. Config-driven overrides
// land in Step 7; until then these are the only knobs and they're
// reasonable for any wiki size below the millions of pages.
const (
	defaultCloudRefresh   = 5 * time.Minute
	defaultRecentsRefresh = 30 * time.Second

	// defaultCloudSize matches the plan's cloud_size default. The
	// top-K selection is the only knob that materially affects the
	// rendered digest's word density; everything else is plumbing.
	defaultCloudSize = 50
)

// Manager runs the two background tickers (cloud rebuild + recents
// flush) for a single wiki. Construct one with NewManager, hand its
// Start a context tied to the process lifetime, and call Stop before
// closing the wiki — closing the wiki out from under a mid-rebuild
// ticker is a `sql: database is closed` race waiting to happen.
//
// Safe for concurrent Start/Stop (idempotent via sync.Once); a single
// Manager is one-shot — once Stop has been called, the Manager cannot
// be Started again. Construct a fresh one if you need a restart.
type Manager struct {
	w *wiki.Wiki

	cloudRefresh   time.Duration
	recentsRefresh time.Duration
	cloudSize      int
	stopwordsExtra []string

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

// Options tunes Manager behavior. Zero-value Options uses the
// package defaults (5m cloud rebuild, 30s recents flush, top-50
// cloud terms). Step 7 will wire these through config.json.
type Options struct {
	CloudRefresh   time.Duration
	RecentsRefresh time.Duration
	CloudSize      int
	// StopwordsExtra appends to the built-in English stopword list.
	// Mirrors plan's digest.stopwords_extra config knob.
	StopwordsExtra []string
}

// NewManager constructs an unstarted Manager. Pass zero Options for
// defaults.
func NewManager(w *wiki.Wiki, opts Options) *Manager {
	if opts.CloudRefresh <= 0 {
		opts.CloudRefresh = defaultCloudRefresh
	}
	if opts.RecentsRefresh <= 0 {
		opts.RecentsRefresh = defaultRecentsRefresh
	}
	if opts.CloudSize <= 0 {
		opts.CloudSize = defaultCloudSize
	}
	return &Manager{
		w:              w,
		cloudRefresh:   opts.CloudRefresh,
		recentsRefresh: opts.RecentsRefresh,
		cloudSize:      opts.CloudSize,
		stopwordsExtra: opts.StopwordsExtra,
	}
}

// Start kicks off the two tickers. Idempotent: a second call is a
// no-op. Returns immediately after spawning goroutines; use Stop to
// wait for clean shutdown.
//
// The cloud is rebuilt synchronously once before the goroutine loop
// starts so a freshly-opened wiki has cloud terms in its digest
// without a 5-minute warm-up. On cold start over a 1k-page wiki this
// takes < 100ms; we accept that latency on Start so the first
// post-open digest read is useful.
func (m *Manager) Start(ctx context.Context) {
	m.startOnce.Do(func() {
		ctx, m.cancel = context.WithCancel(ctx)
		m.done = make(chan struct{})

		// Synchronous first build so cold-start digests have an
		// About: line. We deliberately don't gate on whether a
		// persisted cloud was loaded: even if it was, the on-disk
		// content may have shifted while the server was off, and
		// the cost is small. A failure here logs and continues —
		// the tickers below will retry.
		m.rebuildCloud(ctx)

		go m.run(ctx)
		slog.Info("digest manager started",
			slog.Duration("cloud_refresh", m.cloudRefresh),
			slog.Duration("recents_refresh", m.recentsRefresh),
			slog.Int("cloud_size", m.cloudSize),
		)
	})
}

// Stop cancels the tickers and blocks until the loop goroutine has
// exited. Idempotent. Safe to call after Start, after another Stop,
// or even without ever calling Start (in which case it returns
// immediately).
//
// A final recents flush runs as the loop exits so the last few touches
// between ticker fires aren't lost on shutdown. The Wiki's own Close()
// also calls persistRecents — both paths converge on the same row,
// and the SQLite write is atomic, so the redundancy is harmless.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		if m.cancel == nil {
			return // Stop without Start: nothing to do.
		}
		m.cancel()
		<-m.done
		slog.Info("digest manager stopped")
	})
}

// run is the goroutine that drives both tickers. The cloud rebuild
// is much heavier than the recents flush, but both are well below the
// 30s recents tick on any reasonable wiki size, so a shared goroutine
// with two tickers is simpler than two goroutines and adequately
// non-blocking for the workload.
func (m *Manager) run(ctx context.Context) {
	defer close(m.done)

	cloudTick := time.NewTicker(m.cloudRefresh)
	defer cloudTick.Stop()
	recentsTick := time.NewTicker(m.recentsRefresh)
	defer recentsTick.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush so we don't lose the last ~30s of
			// touches. Use a detached background context: the
			// loop's ctx is already cancelled, but the DB write
			// itself should still get a chance to complete.
			m.flushRecents(context.Background())
			return
		case <-cloudTick.C:
			m.rebuildCloud(ctx)
		case <-recentsTick.C:
			m.flushRecents(ctx)
		}
	}
}

// rebuildCloud runs one cloud rebuild + persistence cycle. Failures
// are logged and swallowed — the digest must degrade gracefully on
// transient errors rather than crashing a long-running service.
func (m *Manager) rebuildCloud(ctx context.Context) {
	start := time.Now()
	terms, err := m.w.BuildCloud(ctx, m.cloudSize, m.stopwordsExtra)
	if err != nil {
		slog.Warn("digest cloud rebuild failed", slog.Any("error", err))
		return
	}
	m.w.SetCloud(terms)
	if err := m.w.PersistCloud(ctx); err != nil {
		slog.Warn("digest cloud persist failed", slog.Any("error", err))
	}
	slog.Info("digest cloud rebuilt",
		slog.Int("terms", len(terms)),
		slog.Duration("elapsed", time.Since(start)),
	)
}

// flushRecents writes the LRU to wiki_state if it's been touched
// since the last write. The dirty gate avoids gratuitous SQLite writes
// on an idle server.
func (m *Manager) flushRecents(ctx context.Context) {
	if !m.w.RecentsDirty() {
		return
	}
	if err := m.w.PersistRecents(ctx); err != nil {
		slog.Warn("digest recents persist failed", slog.Any("error", err))
	}
}
