package wiki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Asset support.
//
// Images and other binary references embedded in markdown live on disk
// in a per-page "sidecar" directory: a page at `foo/bar` keeps its
// assets in `foo/bar.assets/`. The asset path stored in markdown
// (`![](foo/bar.assets/diagram.png)`) is the same string used as the
// link table's `target` for kind='image' rows, which lets every
// lifecycle question be answered by a plain index query.
//
// This file holds the wiki-internal asset CRUD. The MCP and HTTP
// layers wrap these methods; they don't reach into the filesystem
// directly.

const (
	// assetsSuffix is the dirname suffix appended to a page path to
	// form its sidecar asset directory. The "." prefix matches how
	// agents and humans naturally read the references — "this page's
	// assets" — and keeps the directory next to the page on disk.
	assetsSuffix = ".assets"

	// defaultMaxAssetBytes is the per-upload size cap used when the
	// wiki hasn't been configured otherwise. 10 MB matches the design
	// doc default. Operators can raise this via Wiki.MaxAssetBytes if
	// they're hosting larger illustrations.
	defaultMaxAssetBytes int64 = 10 * 1024 * 1024
)

// ErrAssetTooLarge is returned by UploadAsset when content exceeds
// the configured size cap.
var ErrAssetTooLarge = errors.New("asset exceeds size cap")

// ErrUnsupportedAssetType is returned by UploadAsset when the content's
// detected MIME type isn't one of the browser-renderable image formats.
var ErrUnsupportedAssetType = errors.New("unsupported asset type")

// ErrAssetNotFound is returned by ReadAsset and StatAsset when the
// requested asset doesn't exist on disk.
var ErrAssetNotFound = errors.New("asset not found")

// AssetInfo describes an asset without including its body. Returned by
// StatAsset and used to populate the metadata-only mode of the page
// read tools.
type AssetInfo struct {
	// Path is the asset's wiki-relative path as it appears in markdown
	// references and in the links table (kind='image' target).
	Path string `json:"path"`
	// SizeBytes is the on-disk size of the asset file.
	SizeBytes int64 `json:"size_bytes"`
	// MIME is the detected content type. Always populated when the
	// file exists and can be sniffed.
	MIME string `json:"mime"`
}

// UploadAsset writes binary content into the sidecar directory of the
// given page, returning the asset's wiki-relative path on success. The
// returned path is what the caller should embed in markdown:
//
//	uploaded, _ := w.UploadAsset(ctx, "projects/mind-map", "diagram.png", bytes)
//	// uploaded == "projects/mind-map.assets/diagram.png"
//	body := "![diagram](" + uploaded + ")"
//
// Behavior:
//
//   - The sidecar directory is created if it doesn't exist.
//   - If a file with the same name already exists, UploadAsset auto-
//     suffixes the basename ("diagram.png" → "diagram-1.png") and keeps
//     trying until it finds a free slot. Agents don't have to probe.
//   - Content is sniffed via http.DetectContentType plus an SVG check;
//     anything outside the browser-renderable image set is rejected
//     with ErrUnsupportedAssetType.
//   - Size is bounded by Wiki.MaxAssetBytes (default 10 MB), rejecting
//     oversize uploads with ErrAssetTooLarge.
//
// UploadAsset does NOT touch the markdown of any page. It writes the
// file and returns the path; the caller updates the page via
// update_page / edit_page so the reference is captured by the index
// on the next indexPage call.
func (w *Wiki) UploadAsset(ctx context.Context, page, name string, content []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	page, err := normalizePagePath(page)
	if err != nil {
		return "", fmt.Errorf("page: %w", err)
	}

	cleanName, err := sanitizeAssetFilename(name)
	if err != nil {
		return "", err
	}

	maxBytes := w.MaxAssetBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxAssetBytes
	}
	if int64(len(content)) > maxBytes {
		return "", fmt.Errorf("%w: %d bytes > %d", ErrAssetTooLarge, len(content), maxBytes)
	}

	mime, ok := detectImageMIME(content)
	if !ok {
		return "", fmt.Errorf("%w: detected %q", ErrUnsupportedAssetType, mime)
	}

	sidecarRel := page + assetsSuffix
	sidecarAbs := filepath.Join(w.root, sidecarRel)
	if err := os.MkdirAll(sidecarAbs, 0o755); err != nil {
		return "", fmt.Errorf("create sidecar: %w", err)
	}

	finalName, err := resolveAssetCollision(sidecarAbs, cleanName)
	if err != nil {
		return "", err
	}

	absPath := filepath.Join(sidecarAbs, finalName)
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		return "", fmt.Errorf("write asset: %w", err)
	}

	relPath := path.Join(sidecarRel, finalName)
	slog.Info("asset uploaded",
		slog.String("page", page),
		slog.String("path", relPath),
		slog.Int("bytes", len(content)),
		slog.String("mime", mime),
	)
	return relPath, nil
}

// ReadAsset returns the bytes and detected MIME type for an asset path.
// The path must be wiki-relative (as stored in markdown references); it
// is validated against the wiki root to prevent traversal.
func (w *Wiki) ReadAsset(ctx context.Context, assetPath string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}

	abs, err := w.resolveAssetPath(assetPath)
	if err != nil {
		return nil, "", err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("%w: %s", ErrAssetNotFound, assetPath)
		}
		return nil, "", err
	}

	mime, _ := detectImageMIME(data)
	if mime == "" {
		// Fall back to a generic detect so we still serve something
		// useful for assets that pre-date the sniff list (e.g. a
		// human-uploaded format we haven't enumerated). The static
		// handler can still return the bytes; only uploads are gated.
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// StatAsset returns metadata for an asset without reading its full body.
// Used by the metadata-only mode of the page read tools and by future
// listing/GC operations.
func (w *Wiki) StatAsset(ctx context.Context, assetPath string) (*AssetInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	abs, err := w.resolveAssetPath(assetPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrAssetNotFound, assetPath)
		}
		return nil, err
	}

	// We need the head of the file to sniff a MIME, but full read just
	// for stats would be wasteful for large illustrations. 512 bytes is
	// the limit http.DetectContentType actually inspects.
	head := make([]byte, 512)
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, _ := f.Read(head)
	mime, _ := detectImageMIME(head[:n])
	if mime == "" {
		mime = http.DetectContentType(head[:n])
	}

	return &AssetInfo{
		Path:      filepath.ToSlash(assetPath),
		SizeBytes: info.Size(),
		MIME:      mime,
	}, nil
}

// resolveAssetPath validates a wiki-relative asset path and returns its
// absolute filesystem path. Rejects traversal attempts (..) and any
// path that doesn't resolve under the wiki root.
//
// Unlike normalizePagePath, asset paths keep their extension and don't
// have a trailing-slash normalization to do — they're filesystem paths,
// not page paths.
func (w *Wiki) resolveAssetPath(assetPath string) (string, error) {
	if assetPath == "" {
		return "", fmt.Errorf("asset path is empty")
	}
	// Normalize separators and reject leading slashes; asset paths are
	// always wiki-root-relative.
	p := strings.ReplaceAll(assetPath, `\`, "/")
	p = strings.TrimPrefix(p, "/")
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("invalid asset path: %q", assetPath)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("asset path escapes wiki root: %q", assetPath)
	}

	abs := filepath.Join(w.root, filepath.FromSlash(cleaned))
	// Final guard: filepath.Join can't undo a cleaned ".." check above,
	// but defense in depth: resolve symlinks and verify containment.
	absClean, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absClean+string(filepath.Separator), w.root+string(filepath.Separator)) && absClean != w.root {
		return "", fmt.Errorf("asset path escapes wiki root: %q", assetPath)
	}
	return absClean, nil
}

// sanitizeAssetFilename strips path components and forbidden characters
// from a user-supplied filename. Only the basename survives — agents
// can't smuggle "../" or nested directory paths in via the name field.
func sanitizeAssetFilename(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("asset name is empty")
	}
	// Collapse any path-y syntax to just the final component.
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "", fmt.Errorf("invalid asset name: %q", name)
	}
	// SQLite stores the asset path as part of the links table primary
	// key tuple; null bytes would torch sqlite tooling. Reject them.
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("asset name contains NUL")
	}
	return name, nil
}

// resolveAssetCollision returns a free filename inside dir, starting
// from desired and auto-suffixing on collision: "a.png" → "a-1.png" →
// "a-2.png" ... Comparisons are case-insensitive so we don't end up
// with "Diagram.png" and "diagram.png" coexisting on case-sensitive
// filesystems and confusing sync to a case-insensitive remote.
func resolveAssetCollision(dir, desired string) (string, error) {
	existing, err := caseInsensitiveDirSet(dir)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(desired)
	if _, taken := existing[lower]; !taken {
		return desired, nil
	}

	ext := filepath.Ext(desired)
	stem := strings.TrimSuffix(desired, ext)
	for i := 1; i < 10_000; i++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, taken := existing[strings.ToLower(candidate)]; !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free filename after 10000 attempts: %s", desired)
}

// caseInsensitiveDirSet returns the lowercased names of files in dir.
// Missing dirs return an empty set without an error — that's the
// "no collisions, first upload" case.
func caseInsensitiveDirSet(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	set := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		set[strings.ToLower(e.Name())] = struct{}{}
	}
	return set, nil
}

// detectImageMIME inspects the leading bytes of a file and returns the
// MIME type if it's a browser-renderable image format, or ("", false)
// otherwise. SVG gets special-cased because http.DetectContentType
// reports it as a generic XML type; we look for the <svg root element.
//
// Supported set tracks what browsers render natively, not a hand-picked
// subset: PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, ICO. New formats can
// be added by extending the switch below.
func detectImageMIME(content []byte) (string, bool) {
	if len(content) == 0 {
		return "", false
	}

	detected := http.DetectContentType(content)
	switch {
	case strings.HasPrefix(detected, "image/png"):
		return "image/png", true
	case strings.HasPrefix(detected, "image/jpeg"):
		return "image/jpeg", true
	case strings.HasPrefix(detected, "image/gif"):
		return "image/gif", true
	case strings.HasPrefix(detected, "image/webp"):
		return "image/webp", true
	case strings.HasPrefix(detected, "image/bmp"):
		return "image/bmp", true
	case strings.HasPrefix(detected, "image/vnd.microsoft.icon"),
		strings.HasPrefix(detected, "image/x-icon"):
		return "image/x-icon", true
	}

	// AVIF / HEIF: http.DetectContentType returns
	// "application/octet-stream" for these. Sniff the ISO BMFF box
	// header: bytes [4:8] are "ftyp", [8:12] is the brand. AVIF
	// brands include "avif", "avis", "mif1" (HEIF parent), "msf1".
	if len(content) >= 12 && bytes.Equal(content[4:8], []byte("ftyp")) {
		brand := string(content[8:12])
		switch brand {
		case "avif", "avis":
			return "image/avif", true
		}
	}

	// SVG sniff: skip leading whitespace and optional XML decl/DOCTYPE,
	// then look for the <svg element. We deliberately don't fully
	// parse XML — that's the renderer's job. We just need to be
	// confident this is SVG before accepting it.
	if looksLikeSVG(content) {
		return "image/svg+xml", true
	}

	return detected, false
}

// looksLikeSVG checks whether content starts with markup that opens an
// <svg ...> element, allowing leading whitespace, XML declaration, and
// DOCTYPE/comments. It does NOT validate the XML; that's left to the
// downstream renderer.
func looksLikeSVG(content []byte) bool {
	s := bytes.TrimLeft(content, " \t\r\n\xef\xbb\xbf")
	// Strip leading <?xml ...?> declaration if present.
	if bytes.HasPrefix(s, []byte("<?xml")) {
		end := bytes.Index(s, []byte("?>"))
		if end < 0 {
			return false
		}
		s = bytes.TrimLeft(s[end+2:], " \t\r\n")
	}
	// Strip leading comments and DOCTYPE/whitespace until we hit a
	// real element open. Bounded loop so a pathological input can't
	// spin us.
	for i := 0; i < 8; i++ {
		s = bytes.TrimLeft(s, " \t\r\n")
		switch {
		case bytes.HasPrefix(s, []byte("<!--")):
			end := bytes.Index(s, []byte("-->"))
			if end < 0 {
				return false
			}
			s = s[end+3:]
		case bytes.HasPrefix(s, []byte("<!DOCTYPE")):
			end := bytes.IndexByte(s, '>')
			if end < 0 {
				return false
			}
			s = s[end+1:]
		default:
			i = 8 // break the for
		}
	}
	s = bytes.TrimLeft(s, " \t\r\n")
	if !bytes.HasPrefix(s, []byte("<svg")) {
		return false
	}
	// Require the next byte to be whitespace or `>` so we don't
	// accept `<svgfoo>` or `<svg-name>` as SVG.
	if len(s) <= 4 {
		return false
	}
	next := s[4]
	return next == ' ' || next == '\t' || next == '\r' || next == '\n' || next == '>' || next == '/'
}

// gcSidecarAssets removes files under <page>.assets/ that have no
// row in the link index. Called from DeletePage (after the page's
// own rows are deleted from `links`) and from MovePage (to clean up
// any orphans the move left behind). The sidecar dir itself is
// removed if empty after the sweep so the wiki tree stays tidy.
//
// Files referenced by OTHER pages (kind='image' rows with a different
// source) are kept in place — the design intentionally has no shared
// asset pool, so the file lives in its original sidecar even when
// shared. The markdown path in the referencing page still resolves.
func (w *Wiki) gcSidecarAssets(ctx context.Context, page string) error {
	sidecarRel := page + assetsSuffix
	sidecarAbs := filepath.Join(w.root, sidecarRel)

	entries, err := os.ReadDir(sidecarAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// We don't support nested sidecars; skip any
			// subdirectory rather than recursing or deleting it.
			continue
		}
		assetRel := path.Join(sidecarRel, entry.Name())
		var n int
		if err := w.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM links WHERE target = ? AND kind = 'image'",
			assetRel,
		).Scan(&n); err != nil {
			return fmt.Errorf("query asset refs %q: %w", assetRel, err)
		}
		if n > 0 {
			continue
		}
		assetAbs := filepath.Join(sidecarAbs, entry.Name())
		if err := os.Remove(assetAbs); err != nil && !os.IsNotExist(err) {
			slog.Warn("sidecar asset remove failed",
				slog.String("asset", assetRel),
				slog.Any("error", err),
			)
		}
	}

	// If the sidecar is now empty, drop the directory. os.Remove
	// fails on non-empty dirs, which is exactly the "still has shared
	// or human-added files" case we want to leave alone.
	if err := os.Remove(sidecarAbs); err != nil && !os.IsNotExist(err) {
		// Non-empty or permission error — not fatal, just log.
		slog.Debug("sidecar dir not removed (likely non-empty)",
			slog.String("dir", sidecarRel),
			slog.Any("error", err),
		)
	}
	return nil
}

// splitSidecarOnMove rewrites the image references inside a page's
// markdown when the page is moved from→to, and decides which sidecar
// files travel with the page vs. stay behind for other referencers.
//
// Returns the rewritten body. The caller is responsible for writing
// the file at its new path and re-indexing.
//
// Design (option (a) from the image-support discussion): use the link
// index to find which assets are exclusive to the moving page. Move
// those into the new sidecar (rewriting in-body paths to match);
// leave shared assets in the old sidecar (keeping their original path
// in the in-body markdown, since other pages still reference them at
// that path). After the move, gcSidecarAssets on the old sidecar
// cleans up: the dir is gone if everything moved, or it survives
// holding only the shared files.
//
// `oldImages` is the set of image targets the page referenced before
// the move (queried before we delete the source page's link rows).
// We need it as input because by the time we rewrite the body those
// rows may already be gone, and rebuilding it from the new markdown
// is the wrong direction — we need pre-move state.
func (w *Wiki) splitSidecarOnMove(ctx context.Context, from, to string, body []byte, oldImages []string) ([]byte, error) {
	if len(oldImages) == 0 {
		return body, nil
	}

	oldSidecarPrefix := from + assetsSuffix + "/"
	newSidecarRel := to + assetsSuffix
	newSidecarAbs := filepath.Join(w.root, newSidecarRel)

	rewritten := body
	for _, target := range oldImages {
		// Only touch assets that lived in the page's own sidecar.
		// Images referencing some OTHER page's sidecar (cross-
		// referenced images) keep their path either way: the file
		// stays where it was and the path is still valid post-move.
		if !strings.HasPrefix(target, oldSidecarPrefix) {
			continue
		}

		// Is anyone else (besides `from`) referencing this asset?
		var n int
		if err := w.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM links WHERE target = ? AND kind = 'image' AND source != ?",
			target, from,
		).Scan(&n); err != nil {
			return nil, fmt.Errorf("query shared %q: %w", target, err)
		}
		if n > 0 {
			// Shared: leave the file in place, leave the
			// markdown path alone. Other pages still resolve.
			continue
		}

		// Exclusive: move the file to the new sidecar and rewrite
		// the in-body reference.
		basename := path.Base(target)
		oldAbs := filepath.Join(w.root, filepath.FromSlash(target))
		newRel := path.Join(newSidecarRel, basename)
		newAbs := filepath.Join(newSidecarAbs, basename)

		if err := os.MkdirAll(newSidecarAbs, 0o755); err != nil {
			return nil, fmt.Errorf("create new sidecar: %w", err)
		}
		// Best-effort rename; if the source vanished (e.g. someone
		// hand-deleted it while the page still referenced it) we
		// still want the body rewrite to happen so the index is
		// consistent. The next reindex/Stat will surface the
		// missing-file condition if needed.
		if err := os.Rename(oldAbs, newAbs); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("move asset %q: %w", target, err)
		}

		rewritten = bytes.ReplaceAll(rewritten, []byte(target), []byte(newRel))
	}

	return rewritten, nil
}
