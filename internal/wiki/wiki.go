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
	root string   // absolute path to wiki directory
	db   *sql.DB  // SQLite database with FTS5
	mu   sync.RWMutex
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
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	w := &Wiki{root: absRoot, db: db}
	if err := w.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	if err := w.Reindex(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("initial index: %w", err)
	}

	slog.Info("wiki opened", slog.String("root", absRoot))
	return w, nil
}

// Close releases the database connection.
func (w *Wiki) Close() error {
	slog.Info("wiki closing", slog.String("root", w.root))
	return w.db.Close()
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

	CREATE TABLE IF NOT EXISTS links (
		source TEXT NOT NULL,
		target TEXT NOT NULL,
		PRIMARY KEY (source, target)
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
	_, err := w.db.Exec(schema)
	return err
}
