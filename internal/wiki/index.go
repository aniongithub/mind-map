package wiki

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reindex scans the wiki directory and rebuilds the entire index.
func (w *Wiki) Reindex() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear existing data
	if _, err := tx.Exec("DELETE FROM links"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM pages"); err != nil {
		return err
	}

	// Walk the filesystem
	err = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden dirs and files
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
		// Normalize to forward slashes, strip .md extension
		pagePath := strings.TrimSuffix(filepath.ToSlash(rel), ".md")

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		parsed := parsePage(raw)
		if parsed.title == "" {
			parsed.title = filepath.Base(pagePath)
		}

		metaJSON, _ := json.Marshal(parsed.frontmatter)

		_, err = tx.Exec(
			"INSERT OR REPLACE INTO pages (path, title, body, meta, modified) VALUES (?, ?, ?, ?, ?)",
			pagePath, parsed.title, parsed.body, string(metaJSON), info.ModTime().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("index %s: %w", pagePath, err)
		}

		for _, target := range parsed.links {
			_, err = tx.Exec(
				"INSERT OR IGNORE INTO links (source, target) VALUES (?, ?)",
				pagePath, target,
			)
			if err != nil {
				return fmt.Errorf("index link %s->%s: %w", pagePath, target, err)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	return tx.Commit()
}

// indexPage indexes a single page (after write/update).
func (w *Wiki) indexPage(pagePath string) error {
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

	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		"INSERT OR REPLACE INTO pages (path, title, body, meta, modified) VALUES (?, ?, ?, ?, ?)",
		pagePath, parsed.title, parsed.body, string(metaJSON), info.ModTime().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}

	// Rebuild links for this page
	if _, err := tx.Exec("DELETE FROM links WHERE source = ?", pagePath); err != nil {
		return err
	}
	for _, target := range parsed.links {
		if _, err := tx.Exec("INSERT OR IGNORE INTO links (source, target) VALUES (?, ?)", pagePath, target); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// removePageIndex removes a page from the index.
func (w *Wiki) removePageIndex(pagePath string) error {
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM pages WHERE path = ?", pagePath); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM links WHERE source = ?", pagePath); err != nil {
		return err
	}

	return tx.Commit()
}
