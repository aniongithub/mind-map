package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aniongithub/mind-map/internal/share"
	"github.com/aniongithub/mind-map/internal/wiki"
)

// registerExport wires the export routes. Called from register().
func (s *Server) registerExport(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/export/formats", s.getExportFormats)
	mux.HandleFunc("GET /api/export", s.getExport)
}

// getExportFormats handles GET /api/export/formats. Returns the list of
// registered export formats with their settings schemas so the UI can
// render format-specific options.
func (s *Server) getExportFormats(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, share.Formats())
}

// getExport handles GET /api/export. Streams an exported file in the
// requested format.
//
// Query parameters:
//   - format (required): the sharer name (e.g. "zip")
//   - page (required): starting page path for link traversal
//   - depth (optional): link-follow depth (-1 = unlimited, 0 = just this
//     page, 1 = page + its links, etc.). Defaults to 0.
//   - all other params become plugin settings
func (s *Server) getExport(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()

	format := r.URL.Query().Get("format")
	if format == "" {
		http.Error(rw, "format parameter is required", http.StatusBadRequest)
		return
	}

	sharer := share.Get(format)
	if sharer == nil {
		http.Error(rw, fmt.Sprintf("unknown export format: %q", format), http.StatusBadRequest)
		return
	}

	page := r.URL.Query().Get("page")
	if page == "" {
		http.Error(rw, "page parameter is required", http.StatusBadRequest)
		return
	}

	depth := 0
	if d := r.URL.Query().Get("depth"); d != "" {
		parsed, err := strconv.Atoi(d)
		if err != nil {
			http.Error(rw, "depth must be an integer", http.StatusBadRequest)
			return
		}
		depth = parsed
	}

	// Build plugin-specific settings from remaining query params
	settings := make(map[string]any)
	reserved := map[string]bool{"format": true, "page": true, "depth": true}
	for key, values := range r.URL.Query() {
		if reserved[key] || len(values) == 0 {
			continue
		}
		val := values[0]
		if val == "true" || val == "false" {
			settings[key] = val == "true"
		} else if n, err := strconv.Atoi(val); err == nil {
			settings[key] = n
		} else {
			settings[key] = val
		}
	}

	cfg := share.ShareConfig{
		Format:   format,
		Page:     page,
		Depth:    depth,
		Settings: settings,
	}

	// Gather pages via link-graph traversal
	exportPages, err := s.deps.Wiki.ExportPages(r.Context(), page, depth)
	if err != nil {
		http.Error(rw, "export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert wiki.ExportPage to share.Page
	pages := make([]share.Page, len(exportPages))
	for i, ep := range exportPages {
		pages[i] = share.Page{
			Path:        ep.Path,
			Title:       ep.Title,
			Body:        ep.Body,
			Frontmatter: ep.Frontmatter,
			ModifiedAt:  ep.ModifiedAt,
			ImageRefs:   ep.ImageRefs,
		}
	}

	req := share.ExportRequest{
		Config: cfg,
		Pages:  pages,
		Assets: &wikiAssetReader{wiki: s.deps.Wiki, ctx: r.Context()},
	}

	// Build filename for Content-Disposition
	filename := exportFilename(page, sharer.FileExtension())

	rw.Header().Set("Content-Type", sharer.ContentType())
	rw.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))

	if err := sharer.Export(r.Context(), rw, req); err != nil {
		slog.Error("export stream failed",
			slog.String("format", format),
			slog.String("page", page),
			slog.Any("error", err),
		)
		return
	}

	slog.Info("export completed",
		slog.String("format", format),
		slog.String("page", page),
		slog.Int("depth", depth),
		slog.Int("pages", len(pages)),
		slog.Duration("elapsed", time.Since(start)),
	)
}

// wikiAssetReader adapts the wiki to the share.AssetReader interface.
type wikiAssetReader struct {
	wiki *wiki.Wiki
	ctx  context.Context
}

func (r *wikiAssetReader) ReadAsset(_ context.Context, path string) ([]byte, string, error) {
	return r.wiki.ReadAsset(r.ctx, path)
}

// exportFilename builds a suitable download filename from page path and extension.
func exportFilename(page, ext string) string {
	clean := strings.ReplaceAll(page, "/", "-")
	return clean + ext
}
