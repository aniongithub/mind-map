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

// ReindexStats summarizes what a Reindex pass did. All counts are
// across the entire wiki tree on disk; `Total` is the number of
// markdown files found, not the number of changed pages.
type ReindexStats struct {
	Total     int           `json:"total"`
	Added     int           `json:"added"`
	Updated   int           `json:"updated"`
	Removed   int           `json:"removed"`
	Unchanged int           `json:"unchanged"`
	Elapsed   time.Duration `json:"-"`
	// ElapsedMs mirrors Elapsed in a JSON-friendly form so the HTTP
	// endpoint and MCP tool can return it directly.
	ElapsedMs int64 `json:"elapsed_ms"`
}

// Reindex performs an incremental sync of the filesystem with the index.
// It only re-indexes pages whose mtime has changed, adds new pages, and
// removes index entries for deleted files. The lock is held per-page
// rather than for the entire operation, so the server stays responsive.
//
// Returns ReindexStats summarizing the pass so callers can surface the
// result (HTTP endpoint, MCP tool, settings UI). The stats are also
// logged at INFO regardless of caller, matching the prior behavior.
func (w *Wiki) Reindex(ctx context.Context) (ReindexStats, error) {
	if err := ctx.Err(); err != nil {
		return ReindexStats{}, err
	}

	start := time.Now()

	// Phase 1: collect indexed pages
	indexed := make(map[string]string) // path -> modified (RFC3339)
	rows, err := w.db.QueryContext(ctx, "SELECT path, modified FROM pages")
	if err != nil {
		return ReindexStats{}, err
	}
	for rows.Next() {
		var path, modified string
		if err := rows.Scan(&path, &modified); err != nil {
			continue
		}
		indexed[path] = modified
	}
	rows.Close()

	// Phase 2: walk filesystem without holding any lock
	diskPages := make(map[string]os.FileInfo)
	err = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		rel, err := filepath.Rel(w.root, path)
		if err != nil {
			return err
		}
		pagePath := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		info, err := d.Info()
		if err != nil {
			return err
		}
		diskPages[pagePath] = info
		return nil
	})
	if err != nil {
		return ReindexStats{}, err
	}

	// Phase 3: index new/changed pages
	var added, updated, removed int
	for pagePath, info := range diskPages {
		if err := ctx.Err(); err != nil {
			return ReindexStats{}, err
		}

		diskMtime := info.ModTime().UTC().Format(time.RFC3339Nano)
		if idxMtime, exists := indexed[pagePath]; exists && idxMtime == diskMtime {
			continue // unchanged
		}

		absPath := filepath.Join(w.root, pagePath+".md")
		raw, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("reindex read error", slog.String("page", pagePath), slog.Any("error", err))
			continue
		}

		parsed := parsePage(raw)
		if parsed.title == "" {
			parsed.title = filepath.Base(pagePath)
		}
		metaJSON, _ := json.Marshal(parsed.frontmatter)

		tx, err := w.db.BeginTx(ctx, nil)
		if err != nil {
			return ReindexStats{}, err
		}

		_, err = tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO pages (path, title, body, meta, modified) VALUES (?, ?, ?, ?, ?)",
			pagePath, parsed.title, parsed.body, string(metaJSON), diskMtime,
		)
		if err != nil {
			tx.Rollback()
			return ReindexStats{}, fmt.Errorf("index %s: %w", pagePath, err)
		}

		if _, err := tx.ExecContext(ctx, "DELETE FROM links WHERE source = ?", pagePath); err != nil {
			tx.Rollback()
			return ReindexStats{}, err
		}
		for _, target := range parsed.links {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO links (source, target) VALUES (?, ?)", pagePath, target); err != nil {
				tx.Rollback()
				return ReindexStats{}, err
			}
		}

		if err := tx.Commit(); err != nil {
			return ReindexStats{}, err
		}

		if _, exists := indexed[pagePath]; exists {
			updated++
		} else {
			added++
		}
	}

	// Phase 4: remove index entries for deleted files
	for pagePath := range indexed {
		if err := ctx.Err(); err != nil {
			return ReindexStats{}, err
		}
		if _, onDisk := diskPages[pagePath]; !onDisk {
			if err := w.removePageIndex(ctx, pagePath); err != nil {
				slog.Warn("reindex remove error", slog.String("page", pagePath), slog.Any("error", err))
				continue
			}
			// Keep the recents LRU consistent with `pages`: a page
			// that vanishes via raw-filesystem delete + reindex
			// (common after `git pull` in sync) must drop from the
			// LRU here, since DeletePage() was never called. Without
			// this hook the digest's "recently active" can point at
			// a 404.
			w.recents.remove(pagePath)
			removed++
		}
	}

	elapsed := time.Since(start)
	stats := ReindexStats{
		Total:     len(diskPages),
		Added:     added,
		Updated:   updated,
		Removed:   removed,
		Unchanged: len(diskPages) - added - updated,
		Elapsed:   elapsed,
		ElapsedMs: elapsed.Milliseconds(),
	}

	slog.Info("reindex complete",
		slog.Int("total", stats.Total),
		slog.Int("added", stats.Added),
		slog.Int("updated", stats.Updated),
		slog.Int("removed", stats.Removed),
		slog.Int("unchanged", stats.Unchanged),
		slog.Duration("elapsed", stats.Elapsed),
	)
	return stats, nil
}

// indexPage indexes a single page (after write/update).
func (w *Wiki) indexPage(ctx context.Context, pagePath string) error {
	absPath := filepath.Join(w.root, pagePath+".md")

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", absPath, err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	parsed := parsePage(raw)
	if parsed.title == "" {
		parsed.title = filepath.Base(pagePath)
	}

	metaJSON, _ := json.Marshal(parsed.frontmatter)

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO pages (path, title, body, meta, modified) VALUES (?, ?, ?, ?, ?)",
		pagePath, parsed.title, parsed.body, string(metaJSON), info.ModTime().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}

	// Rebuild links for this page
	if _, err := tx.ExecContext(ctx, "DELETE FROM links WHERE source = ?", pagePath); err != nil {
		return err
	}
	for _, target := range parsed.links {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO links (source, target) VALUES (?, ?)", pagePath, target); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// removePageIndex removes a page from the index.
func (w *Wiki) removePageIndex(ctx context.Context, pagePath string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM pages WHERE path = ?", pagePath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM links WHERE source = ?", pagePath); err != nil {
		return err
	}

	return tx.Commit()
}
