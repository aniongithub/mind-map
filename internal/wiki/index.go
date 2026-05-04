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

// Reindex performs an incremental sync of the filesystem with the index.
// It only re-indexes pages whose mtime has changed, adds new pages, and
// removes index entries for deleted files. The lock is held per-page
// rather than for the entire operation, so the server stays responsive.
func (w *Wiki) Reindex(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	start := time.Now()

	// Phase 1: collect indexed pages
	indexed := make(map[string]string) // path -> modified (RFC3339)
	rows, err := w.db.QueryContext(ctx, "SELECT path, modified FROM pages")
	if err != nil {
		return err
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
		return err
	}

	// Phase 3: index new/changed pages
	var added, updated, removed int
	for pagePath, info := range diskPages {
		if err := ctx.Err(); err != nil {
			return err
		}

		diskMtime := info.ModTime().UTC().Format(time.RFC3339)
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
			return err
		}

		_, err = tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO pages (path, title, body, meta, modified) VALUES (?, ?, ?, ?, ?)",
			pagePath, parsed.title, parsed.body, string(metaJSON), diskMtime,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("index %s: %w", pagePath, err)
		}

		if _, err := tx.ExecContext(ctx, "DELETE FROM links WHERE source = ?", pagePath); err != nil {
			tx.Rollback()
			return err
		}
		for _, target := range parsed.links {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO links (source, target) VALUES (?, ?)", pagePath, target); err != nil {
				tx.Rollback()
				return err
			}
		}

		if err := tx.Commit(); err != nil {
			return err
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
			return err
		}
		if _, onDisk := diskPages[pagePath]; !onDisk {
			if err := w.removePageIndex(ctx, pagePath); err != nil {
				slog.Warn("reindex remove error", slog.String("page", pagePath), slog.Any("error", err))
				continue
			}
			removed++
		}
	}

	slog.Info("reindex complete",
		slog.Int("total", len(diskPages)),
		slog.Int("added", added),
		slog.Int("updated", updated),
		slog.Int("removed", removed),
		slog.Int("unchanged", len(diskPages)-added-updated),
		slog.Duration("elapsed", time.Since(start)),
	)
	return nil
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
		pagePath, parsed.title, parsed.body, string(metaJSON), info.ModTime().UTC().Format(time.RFC3339),
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
