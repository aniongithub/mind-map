package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GetPage retrieves a single page by path.
func (w *Wiki) GetPage(ctx context.Context, pagePath string) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var title, body, metaStr, modified string
	err := w.db.QueryRowContext(ctx,
		"SELECT title, body, meta, modified FROM pages WHERE path = ?", pagePath,
	).Scan(&title, &body, &metaStr, &modified)
	if err != nil {
		return nil, fmt.Errorf("page not found: %s", pagePath)
	}

	var fm map[string]interface{}
	if err := json.Unmarshal([]byte(metaStr), &fm); err != nil {
		slog.Warn("page metadata parse error", slog.String("page", pagePath), slog.Any("error", err))
	}

	modTime, err := time.Parse(time.RFC3339, modified)
	if err != nil {
		slog.Warn("page modified time parse error", slog.String("page", pagePath), slog.Any("error", err))
	}

	links, err := w.getLinks(ctx, pagePath)
	if err != nil {
		slog.Warn("failed to get links", slog.String("page", pagePath), slog.Any("error", err))
	}
	backlinks, err := w.getBacklinks(ctx, pagePath)
	if err != nil {
		slog.Warn("failed to get backlinks", slog.String("page", pagePath), slog.Any("error", err))
	}

	return &Page{
		Path:        pagePath,
		Title:       title,
		Body:        body,
		Frontmatter: fm,
		Links:       links,
		Backlinks:   backlinks,
		ModifiedAt:  modTime,
	}, nil
}

// ListPages returns all pages, optionally filtered by a prefix path.
func (w *Wiki) ListPages(ctx context.Context, prefix string) ([]Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	query := "SELECT path, title, meta, modified FROM pages"
	var args []interface{}
	if prefix != "" {
		query += " WHERE path LIKE ? OR path = ?"
		args = append(args, prefix+"/%", prefix)
	}
	query += " ORDER BY modified DESC"

	rows, err := w.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pages []Page
	for rows.Next() {
		var p Page
		var metaStr, modified string
		if err := rows.Scan(&p.Path, &p.Title, &metaStr, &modified); err != nil {
			slog.Warn("list pages scan error", slog.Any("error", err))
			continue
		}
		if err := json.Unmarshal([]byte(metaStr), &p.Frontmatter); err != nil {
			slog.Warn("list pages metadata parse error", slog.String("page", p.Path), slog.Any("error", err))
		}
		if t, err := time.Parse(time.RFC3339, modified); err == nil {
			p.ModifiedAt = t
		} else {
			slog.Warn("list pages time parse error", slog.String("page", p.Path), slog.Any("error", err))
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// CreatePage creates a new page with the given content.
func (w *Wiki) CreatePage(ctx context.Context, pagePath string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := w.acquireLock(ctx, pagePath); err != nil {
		return err
	}
	defer w.releaseLock(pagePath)

	absPath := filepath.Join(w.root, pagePath+".md")

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Don't overwrite existing pages
	if _, err := os.Stat(absPath); err == nil {
		return fmt.Errorf("page already exists: %s", pagePath)
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write page: %w", err)
	}

	slog.Info("page created", slog.String("page", pagePath))
	return w.indexPage(ctx, pagePath)
}

// UpdatePage replaces the content of an existing page.
func (w *Wiki) UpdatePage(ctx context.Context, pagePath string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := w.acquireLock(ctx, pagePath); err != nil {
		return err
	}
	defer w.releaseLock(pagePath)

	absPath := filepath.Join(w.root, pagePath+".md")

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return fmt.Errorf("page not found: %s", pagePath)
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write page: %w", err)
	}

	slog.Info("page updated", slog.String("page", pagePath))
	return w.indexPage(ctx, pagePath)
}

// DeletePage removes a page from the filesystem and index.
func (w *Wiki) DeletePage(ctx context.Context, pagePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := w.acquireLock(ctx, pagePath); err != nil {
		return err
	}
	defer w.releaseLock(pagePath)

	absPath := filepath.Join(w.root, pagePath+".md")

	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete page: %w", err)
	}

	slog.Info("page deleted", slog.String("page", pagePath))
	return w.removePageIndex(ctx, pagePath)
}

// Search performs a full-text search across page titles and bodies.
func (w *Wiki) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 20
	}

	rows, err := w.db.QueryContext(ctx, `
		SELECT p.path, p.title, snippet(pages_fts, 2, '<mark>', '</mark>', '…', 32) as snip
		FROM pages_fts
		JOIN pages p ON p.rowid = pages_fts.rowid
		WHERE pages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Path, &r.Title, &r.Snippet); err != nil {
			slog.Warn("search scan error", slog.Any("error", err))
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// GetBacklinks returns paths of pages that link to the given page.
func (w *Wiki) GetBacklinks(ctx context.Context, pagePath string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return w.getBacklinks(ctx, pagePath)
}

// Context returns a WikiContext overview.
func (w *Wiki) Context(ctx context.Context) (*WikiContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var count int
	if err := w.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pages").Scan(&count); err != nil {
		slog.Warn("context page count error", slog.Any("error", err))
	}

	// Recent pages
	rows, err := w.db.QueryContext(ctx, "SELECT path, title, modified FROM pages ORDER BY modified DESC LIMIT 20")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recent []Page
	for rows.Next() {
		var p Page
		var modified string
		if err := rows.Scan(&p.Path, &p.Title, &modified); err != nil {
			slog.Warn("context scan error", slog.Any("error", err))
			continue
		}
		if t, err := time.Parse(time.RFC3339, modified); err == nil {
			p.ModifiedAt = t
		} else {
			slog.Warn("context time parse error", slog.String("page", p.Path), slog.Any("error", err))
		}
		recent = append(recent, p)
	}

	// Top-level dirs
	dirs := w.topLevelDirs()

	return &WikiContext{
		PageCount:    count,
		RecentPages:  recent,
		TopLevelDirs: dirs,
	}, nil
}

// --- locking ---

// acquireLock attempts to acquire a page-level lock via SQLite.
// Returns an error if the page is already locked by another session.
func (w *Wiki) acquireLock(ctx context.Context, pagePath string) error {
	_, err := w.db.ExecContext(ctx,
		"INSERT INTO page_locks (path, holder, acquired) VALUES (?, ?, ?)",
		pagePath, w.sessionID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		// Check if it's our own stale lock
		var holder string
		row := w.db.QueryRowContext(ctx, "SELECT holder FROM page_locks WHERE path = ?", pagePath)
		if row.Scan(&holder) == nil && holder == w.sessionID {
			// Our own lock, refresh it
			w.db.ExecContext(ctx,
				"UPDATE page_locks SET acquired = ? WHERE path = ? AND holder = ?",
				time.Now().UTC().Format(time.RFC3339), pagePath, w.sessionID,
			)
			return nil
		}
		return fmt.Errorf("page '%s' is locked by another session", pagePath)
	}
	return nil
}

// releaseLock releases a page-level lock.
func (w *Wiki) releaseLock(pagePath string) {
	w.db.Exec("DELETE FROM page_locks WHERE path = ? AND holder = ?", pagePath, w.sessionID)
}

// --- internal helpers ---

func (w *Wiki) getLinks(ctx context.Context, pagePath string) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, "SELECT target FROM links WHERE source = ?", pagePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err == nil {
			links = append(links, target)
		}
	}
	return links, nil
}

func (w *Wiki) getBacklinks(ctx context.Context, pagePath string) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, "SELECT source FROM links WHERE target = ?", pagePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backlinks []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err == nil {
			backlinks = append(backlinks, source)
		}
	}
	return backlinks, nil
}

func (w *Wiki) topLevelDirs() []string {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		slog.Warn("failed to read wiki root for top-level dirs", slog.Any("error", err))
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}
