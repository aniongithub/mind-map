// Asset HTTP handlers.
//
// Two endpoints:
//
//   - POST /api/assets — upload an image. Accepts either a JSON body
//     ({page, name, content_base64}) for parity with the MCP tool, or
//     a multipart/form-data body with fields {page, name, file=<binary>}
//     for browser-friendly uploads.
//
//   - GET /assets/<page-path>.assets/<filename> — serve the bytes of an
//     uploaded asset. Lives outside the /api/ prefix so the web UI can
//     reference it directly from <img src> tags rendered by Goldmark.
//     For SVG specifically, a strict Content-Security-Policy is set so
//     embedded scripts and external loads cannot execute.

package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aniongithub/mind-map/internal/wiki"
)

// registerAssets wires the asset routes. Called from register().
func (s *Server) registerAssets(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/assets", s.uploadAsset)
	mux.HandleFunc("DELETE /api/assets/{path...}", s.deleteAsset)
	mux.HandleFunc("GET /assets/{path...}", s.serveAsset)
}

// uploadAssetJSON is the JSON-body shape for asset uploads. Mirrors
// the MCP tool's input so a client picking either transport sees the
// same field names.
type uploadAssetJSON struct {
	Page          string `json:"page"`
	Name          string `json:"name"`
	ContentBase64 string `json:"content_base64"`
}

// uploadAssetResponse is the success payload for POST /api/assets.
type uploadAssetResponse struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	MIME      string `json:"mime"`
}

// uploadAsset handles POST /api/assets. Accepts JSON or multipart bodies.
// The page and name fields are required; content arrives as either
// base64-encoded JSON or a multipart "file" part.
func (s *Server) uploadAsset(rw http.ResponseWriter, r *http.Request) {
	page, name, content, err := readAssetUpload(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	uploaded, err := s.deps.Wiki.UploadAsset(r.Context(), page, name, content)
	if err != nil {
		slog.Warn("http upload_asset failed",
			slog.String("page", page),
			slog.String("name", name),
			slog.Int("bytes", len(content)),
			slog.Any("error", err),
		)
		switch {
		case errors.Is(err, wiki.ErrAssetTooLarge):
			http.Error(rw, err.Error(), http.StatusRequestEntityTooLarge)
		case errors.Is(err, wiki.ErrUnsupportedAssetType):
			http.Error(rw, err.Error(), http.StatusUnsupportedMediaType)
		default:
			http.Error(rw, err.Error(), http.StatusBadRequest)
		}
		return
	}

	info, statErr := s.deps.Wiki.StatAsset(r.Context(), uploaded)
	if statErr != nil {
		slog.Warn("http upload_asset stat failed",
			slog.String("path", uploaded), slog.Any("error", statErr))
		info = &wiki.AssetInfo{Path: uploaded}
	}

	rw.WriteHeader(http.StatusCreated)
	writeJSON(rw, uploadAssetResponse{
		Path:      uploaded,
		URL:       "/assets/" + uploaded,
		SizeBytes: info.SizeBytes,
		MIME:      info.MIME,
	})
}

// readAssetUpload extracts (page, name, content) from either a JSON
// body or a multipart/form-data body. Returns descriptive errors that
// the caller can pass straight to http.Error.
//
// Body size is bounded by http.MaxBytesReader using the wiki's
// MaxAssetBytes (or default) plus a small overhead for the multipart
// framing. Going over the cap is reported as "request entity too
// large" by the standard library.
func readAssetUpload(r *http.Request) (page, name string, content []byte, err error) {
	maxBytes := defaultUploadCapForRequest(r)
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)

	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		// 32 MiB in-memory threshold matches stdlib defaults for
		// form parsing; anything larger gets spooled to a temp
		// file by the multipart reader.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return "", "", nil, errors.New("parse multipart: " + err.Error())
		}
		page = r.FormValue("page")
		name = r.FormValue("name")

		f, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			return "", "", nil, errors.New("missing 'file' part: " + ferr.Error())
		}
		defer f.Close()
		if name == "" {
			name = hdr.Filename
		}
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			return "", "", nil, errors.New("read 'file' part: " + rerr.Error())
		}
		content = data

	default:
		// Treat anything else as JSON for simplicity. application/
		// json is the documented happy path; other content types
		// either decode as JSON or get a clear parse error.
		var body uploadAssetJSON
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			return "", "", nil, errors.New("invalid JSON: " + derr.Error())
		}
		page = body.Page
		name = body.Name
		if body.ContentBase64 == "" {
			return "", "", nil, errors.New("content_base64 is required for JSON uploads")
		}
		data, derr := base64.StdEncoding.DecodeString(body.ContentBase64)
		if derr != nil {
			if alt, altErr := base64.URLEncoding.DecodeString(body.ContentBase64); altErr == nil {
				data = alt
			} else {
				return "", "", nil, errors.New("decode content_base64: " + derr.Error())
			}
		}
		content = data
	}

	if page == "" {
		return "", "", nil, errors.New("page is required")
	}
	if name == "" {
		return "", "", nil, errors.New("name is required")
	}
	return page, name, content, nil
}

// defaultUploadCapForRequest returns the HTTP-level body cap for an
// upload. We don't have a clean handle on Wiki.MaxAssetBytes from
// here without expanding the Deps surface, so we use a generous
// constant upper bound (128 MiB) and let the wiki layer report the
// precise cap to the client when it rejects via ErrAssetTooLarge.
// The cap mostly exists to bound multipart parsing memory.
func defaultUploadCapForRequest(_ *http.Request) int64 {
	return 128 * 1024 * 1024
}

// deleteAsset handles DELETE /api/assets/<path>. Removes the asset
// file (and any index rows referencing it). Pages that still embed
// the asset will have a dangling markdown reference until they are
// edited — the caller is expected to clean those up if it cares.
func (s *Server) deleteAsset(rw http.ResponseWriter, r *http.Request) {
	assetPath := r.PathValue("path")
	if assetPath == "" {
		http.Error(rw, "asset path is required", http.StatusBadRequest)
		return
	}

	if err := s.deps.Wiki.DeleteAsset(r.Context(), assetPath); err != nil {
		if errors.Is(err, wiki.ErrAssetNotFound) {
			http.NotFound(rw, r)
			return
		}
		slog.Warn("http delete_asset failed",
			slog.String("path", assetPath), slog.Any("error", err))
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(rw, map[string]string{"status": "deleted", "path": assetPath})
}

// serveAsset handles GET /assets/<path>. Reads the asset from the
// wiki and streams it back with the correct Content-Type. SVG is
// served with a strict CSP to neutralize script-injection from
// hand-crafted SVG payloads.
//
// The /assets prefix is deliberately distinct from the SPA static
// handler at "/" so URLs in markdown (rewritten by the web UI to
// /assets/<path>) don't conflict with the React/Preact routes.
func (s *Server) serveAsset(rw http.ResponseWriter, r *http.Request) {
	assetPath := r.PathValue("path")
	if assetPath == "" {
		http.NotFound(rw, r)
		return
	}

	data, mime, err := s.deps.Wiki.ReadAsset(r.Context(), assetPath)
	if err != nil {
		if errors.Is(err, wiki.ErrAssetNotFound) {
			http.NotFound(rw, r)
			return
		}
		slog.Warn("http serve_asset failed",
			slog.String("path", assetPath), slog.Any("error", err))
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	rw.Header().Set("Content-Type", mime)
	rw.Header().Set("Cache-Control", "public, max-age=300")
	// Conservative CSP for SVG: no scripts, no external loads, no
	// inline event handlers. Stops script-injection attacks via
	// embedded <script> or javascript: URLs in hand-crafted SVG.
	if strings.HasPrefix(mime, "image/svg") {
		rw.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	// http.ServeContent gives us conditional GET and byte-range
	// support; it needs an io.ReadSeeker, which bytes.NewReader
	// satisfies. We've already loaded the bytes into memory, so
	// wrapping is essentially free.
	http.ServeContent(rw, r, assetPath, time.Time{}, bytes.NewReader(data))
}
