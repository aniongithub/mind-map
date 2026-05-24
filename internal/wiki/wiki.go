// Package wiki implements a markdown-based wiki engine backed by the filesystem
// and indexed with SQLite FTS5. Pages are plain markdown files with optional
// YAML frontmatter. Wikilinks ([[target]]) are first-class citizens — the engine
// extracts them during indexing and maintains a backlink graph.
//
// All public methods are safe for concurrent use.
package wiki

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO required)
)

// Page represents a single wiki page.
type Page struct {
	// Path relative to the wiki root, without extension (e.g. "projects/mind-map")
	Path string `json:"path"`
	// Title extracted from frontmatter or first heading, falling back to filename
	Title string `json:"title"`
	// Raw markdown content (without frontmatter)
	Body string `json:"body"`
	// Parsed YAML frontmatter as key-value pairs
	Frontmatter map[string]interface{} `json:"frontmatter,omitempty"`
	// Outgoing wikilinks (target paths)
	Links []string `json:"links,omitempty"`
	// Incoming links from other pages
	Backlinks []string `json:"backlinks,omitempty"`
	// File modification time
	ModifiedAt time.Time `json:"modified_at"`
}

// SearchResult is a page returned from a search query with a relevance snippet.
type SearchResult struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// WikiContext provides an overview of the wiki for orientation.
type WikiContext struct {
	PageCount    int      `json:"page_count"`
	RecentPages  []Page   `json:"recent_pages"`
	TopLevelDirs []string `json:"top_level_dirs"`
}

// Wiki is the core engine. Create one with Open().
type Wiki struct {
	root      string  // absolute path to wiki directory
	db        *sql.DB // SQLite database with FTS5
	sessionID string  // unique ID for this process, used for page locks
	// recents tracks pages the user/agent has actively touched. See
	// recents.go for the rationale (intent vs. disk mtime). Persistence
	// to SQLite is layered on in state.go; here it just lives in memory.
	recents *recentsLRU
	// cloud holds the most recent word/phrase cloud rebuild. Populated
	// by the 5-minute ticker (Step 6); cold start renders without it.
	cloud *cloudCache
	// digest caches the rendered markdown blob, invalidated by cloud
	// version + recents seq changes. See digest.go.
	digest *digestCache
	// closed guards Close() against double-invocation: testWiki and
	// other callers commonly stack defer Close on top of t.Cleanup.
	// Without this guard, the second Close() runs persistRecents
	// against an already-closed DB and logs a spurious warning.
	closeOnce sync.Once
	closeErr  error
}

// Open opens (or creates) a wiki rooted at the given directory.
// It initializes the SQLite index and performs an initial scan.
func Open(root string) (*Wiki, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve wiki root: %w", err)
	}

	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create wiki dir: %w", err)
	}

	dbPath := filepath.Join(absRoot, ".mind-map.db")
	// recursive_triggers must be ON so that the AFTER DELETE trigger on
	// `pages` fires during INSERT OR REPLACE (used by indexPage/Reindex).
	// Without it, pages_fts accumulates orphan entries — see #35.
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_pragma=recursive_triggers(1)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	sessionID := fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	w := &Wiki{
		root:      absRoot,
		db:        db,
		sessionID: sessionID,
		// Capacity 20 matches the plan default. Step 4 will swap this
		// for a config-driven value (digest.recents_size); the default
		// keeps existing callers unaffected.
		recents: newRecentsLRU(20),
		cloud:   &cloudCache{},
		digest:  &digestCache{},
	}
	if err := w.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// Rebuild pages_fts from the content table once on startup. This
	// purges any orphan FTS rows left behind by earlier versions that
	// ran without recursive_triggers enabled.
	if _, err := db.Exec("INSERT INTO pages_fts(pages_fts) VALUES('rebuild')"); err != nil {
		slog.Warn("pages_fts rebuild failed", slog.Any("error", err))
	}

	if _, err := w.Reindex(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("initial index: %w", err)
	}

	// Load persisted derived state (recents LRU, word cloud) after
	// reindex so any stale entries pointing at pages that vanished
	// while the server was off get filtered against the fresh index.
	// Failures are logged but non-fatal — a corrupt state row just
	// degrades to "fresh-wiki" behavior, not a crash.
	w.loadState(context.Background())

	slog.Info("wiki opened", slog.String("root", absRoot))
	return w, nil
}

// Close releases page locks held by this session and closes the database.
// Idempotent — safe to call multiple times (e.g. when a test stacks
// defer Close on top of testWiki's t.Cleanup).
func (w *Wiki) Close() error {
	w.closeOnce.Do(func() {
		slog.Info("wiki closing", slog.String("root", w.root))
		// Flush the LRU one last time so a clean shutdown doesn't
		// lose the last ~30 seconds of touches between ticker fires.
		// Errors are logged, not propagated — we'd rather close
		// cleanly with a slightly stale snapshot than leak the DB
		// handle.
		if err := w.persistRecents(context.Background()); err != nil {
			slog.Warn("recents flush on close failed", slog.Any("error", err))
		}
		w.db.Exec("DELETE FROM page_locks WHERE holder = ?", w.sessionID)
		w.closeErr = w.db.Close()
	})
	return w.closeErr
}

// Root returns the wiki's root directory.
func (w *Wiki) Root() string {
	return w.root
}

func (w *Wiki) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS pages (
		path      TEXT PRIMARY KEY,
		title     TEXT NOT NULL DEFAULT '',
		body      TEXT NOT NULL DEFAULT '',
		meta      TEXT NOT NULL DEFAULT '{}',
		modified  TEXT NOT NULL DEFAULT ''
	);

	-- The PRIMARY KEY is (source, target) for back-compat with databases
	-- migrated from before the kind column existed (see migrate.go). In
	-- practice (source, target) is unique even across kinds because
	-- wikilink targets are page paths (no extension) while image targets
	-- are filesystem paths with extensions — they don't collide.
	CREATE TABLE IF NOT EXISTS links (
		source TEXT NOT NULL,
		target TEXT NOT NULL,
		kind   TEXT NOT NULL DEFAULT 'link',
		PRIMARY KEY (source, target)
	);

	CREATE TABLE IF NOT EXISTS page_locks (
		path     TEXT PRIMARY KEY,
		holder   TEXT NOT NULL,
		acquired TEXT NOT NULL
	);

	CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(
		path, title, body,
		content='pages',
		content_rowid='rowid'
	);

	-- Triggers to keep FTS in sync
	CREATE TRIGGER IF NOT EXISTS pages_ai AFTER INSERT ON pages BEGIN
		INSERT INTO pages_fts(rowid, path, title, body)
		VALUES (new.rowid, new.path, new.title, new.body);
	END;

	CREATE TRIGGER IF NOT EXISTS pages_ad AFTER DELETE ON pages BEGIN
		INSERT INTO pages_fts(pages_fts, rowid, path, title, body)
		VALUES ('delete', old.rowid, old.path, old.title, old.body);
	END;

	CREATE TRIGGER IF NOT EXISTS pages_au AFTER UPDATE ON pages BEGIN
		INSERT INTO pages_fts(pages_fts, rowid, path, title, body)
		VALUES ('delete', old.rowid, old.path, old.title, old.body);
		INSERT INTO pages_fts(rowid, path, title, body)
		VALUES (new.rowid, new.path, new.title, new.body);
	END;

	CREATE INDEX IF NOT EXISTS idx_links_target ON links(target);
	`
	if _, err := w.db.Exec(schema); err != nil {
		return err
	}

	if err := w.initStateSchema(); err != nil {
		return fmt.Errorf("wiki_state schema: %w", err)
	}

	if err := w.migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Clean up stale locks (older than 5 minutes) from crashed processes
	_, err := w.db.Exec("DELETE FROM page_locks WHERE acquired < ?",
		time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339))
	return err
}
