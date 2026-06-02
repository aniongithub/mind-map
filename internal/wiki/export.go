package wiki

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// ExportPage is a page with full content suitable for export. It includes
// the raw body, frontmatter, and the list of image asset paths referenced.
type ExportPage struct {
	Path        string                 `json:"path"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	Frontmatter map[string]interface{} `json:"frontmatter,omitempty"`
	ModifiedAt  time.Time              `json:"modified_at"`
	ImageRefs   []string               `json:"image_refs,omitempty"`
}

// ExportPages returns pages reachable from startPage by following outgoing
// wikilinks up to the given depth (BFS).
//
// Parameters:
//   - startPage: the page to start from (required)
//   - depth: how many link-hops to follow.
//     0 = just the start page itself
//     1 = start page + pages it links to
//     N = N hops from start
//     -1 = unlimited (follow all reachable links)
func (w *Wiki) ExportPages(ctx context.Context, startPage string, depth int) ([]ExportPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := normalizePagePath(startPage)
	if err != nil {
		return nil, err
	}
	startPage = normalized

	// BFS traversal of the wikilink graph
	visited := map[string]bool{startPage: true}
	queue := []string{startPage}
	currentDepth := 0

	for len(queue) > 0 && (depth < 0 || currentDepth < depth) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Process all pages at the current depth level
		levelSize := len(queue)
		var nextLevel []string
		for i := 0; i < levelSize; i++ {
			links, err := w.getLinks(ctx, queue[i])
			if err != nil {
				slog.Warn("export: failed to get links",
					slog.String("page", queue[i]), slog.Any("error", err))
				continue
			}
			for _, target := range links {
				if !visited[target] {
					visited[target] = true
					nextLevel = append(nextLevel, target)
				}
			}
		}
		queue = nextLevel
		currentDepth++
	}

	// Fetch full content for all visited pages
	var pages []ExportPage
	for path := range visited {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		ep, err := w.exportOnePage(ctx, path)
		if err != nil {
			slog.Warn("export: failed to load page",
				slog.String("page", path), slog.Any("error", err))
			continue
		}
		if ep != nil {
			pages = append(pages, *ep)
		}
	}

	return pages, nil
}

// exportOnePage loads a single page for export. Returns nil if the page
// doesn't exist (dangling link).
func (w *Wiki) exportOnePage(ctx context.Context, pagePath string) (*ExportPage, error) {
	var p ExportPage
	var metaStr, modified string
	err := w.db.QueryRowContext(ctx,
		"SELECT path, title, body, meta, modified FROM pages WHERE path = ?",
		pagePath,
	).Scan(&p.Path, &p.Title, &p.Body, &metaStr, &modified)
	if err != nil {
		// Page might not exist (dangling wikilink) — not an error
		return nil, nil
	}

	if err := json.Unmarshal([]byte(metaStr), &p.Frontmatter); err != nil {
		slog.Warn("export page metadata parse error",
			slog.String("page", p.Path), slog.Any("error", err))
	}
	if t, err := time.Parse(time.RFC3339Nano, modified); err == nil {
		p.ModifiedAt = t
	}

	images, err := w.imageRefsFor(ctx, p.Path)
	if err != nil {
		slog.Warn("export page image refs error",
			slog.String("page", p.Path), slog.Any("error", err))
	}
	p.ImageRefs = images

	return &p, nil
}

