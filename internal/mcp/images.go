// MCP tool handlers for image support.
//
// Three new tools surface the wiki's asset layer to agents:
//
//   - upload_image: write binary content into the page's sidecar and
//     return a markdown-ready path. The agent embeds the reference
//     itself via update_page / edit_page; this keeps tool
//     responsibilities crisp (we considered an insert_image
//     convenience tool but deferred it — see the design doc).
//
//   - download_image: fetch an asset and return it as MCP ImageContent
//     so vision-capable agents see the image inline.
//
//   - get_page and search_pages gain include_images and
//     include_image_metadata flags, opt-in by design (default off so
//     token cost stays predictable). When the operator forces images
//     off (Server.ForceImagesOff) those flags are silently overridden
//     and a notice is appended to the response so callers don't reason
//     as if they got what they asked for.

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ForceImagesOff, when set on the Server, makes get_page and
// search_pages behave as if include_images=false and
// include_image_metadata=false regardless of caller request. Intended
// for token-constrained deployments. See SetForceImagesOff.
func (s *Server) SetForceImagesOff(off bool) { s.forceImagesOff = off }

// uploadImageInput is the request shape for the upload_image tool.
//
// Content is base64-encoded so the JSON-over-stdio MCP transport can
// carry arbitrary binary safely. The Go SDK doesn't currently support
// passing []byte through tool inputs as raw bytes — base64 is the
// universal idiom.
type uploadImageInput struct {
	Page          string `json:"page" jsonschema:"page path (without .md) under which to store the image; the asset lives in <page>.assets/<name>"`
	Name          string `json:"name" jsonschema:"filename for the uploaded image, e.g. diagram.png; collisions auto-suffix"`
	ContentBase64 string `json:"content_base64" jsonschema:"image bytes encoded as base64. Supported formats: PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, ICO."`
}

// uploadImageOutput is the success payload. `Path` is the markdown-
// ready relative path the agent should embed; `URL` is the convenience
// HTTP path served by the static asset handler.
type uploadImageOutput struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	MIME      string `json:"mime"`
}

func (s *Server) uploadImage(ctx context.Context, _ *mcp.CallToolRequest, in uploadImageInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()

	content, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil {
		// Be forgiving of URL-safe base64 too — vision tooling often
		// emits one or the other.
		if alt, altErr := base64.URLEncoding.DecodeString(in.ContentBase64); altErr == nil {
			content = alt
		} else {
			return nil, nil, fmt.Errorf("decode content_base64: %w", err)
		}
	}

	uploaded, err := s.wiki.UploadAsset(ctx, in.Page, in.Name, content)
	if err != nil {
		slog.Warn("tool.upload_image failed",
			slog.String("page", in.Page),
			slog.String("name", in.Name),
			slog.Int("bytes", len(content)),
			slog.Any("error", err),
		)
		switch {
		case errors.Is(err, wiki.ErrAssetTooLarge):
			return nil, nil, fmt.Errorf("%w. Compress or split the image before retrying", err)
		case errors.Is(err, wiki.ErrUnsupportedAssetType):
			return nil, nil, fmt.Errorf("%w. Supported formats: PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, ICO", err)
		}
		return nil, nil, err
	}

	info, err := s.wiki.StatAsset(ctx, uploaded)
	if err != nil {
		// We just wrote the file, so stat failing is genuinely
		// unexpected. Surface a usable response anyway — the
		// upload itself succeeded.
		slog.Warn("tool.upload_image stat after upload failed",
			slog.String("path", uploaded), slog.Any("error", err))
		info = &wiki.AssetInfo{Path: uploaded}
	}

	out := uploadImageOutput{
		Path:      uploaded,
		URL:       "/api/assets/" + uploaded,
		SizeBytes: info.SizeBytes,
		MIME:      info.MIME,
	}
	slog.Info("tool.upload_image",
		slog.String("page", in.Page),
		slog.String("path", uploaded),
		slog.Int64("bytes", info.SizeBytes),
		slog.String("mime", info.MIME),
		slog.Duration("elapsed", time.Since(start)),
	)
	return textResult(out)
}

// downloadImageInput is the request shape for download_image. The path
// is the wiki-relative asset path (as it appears in markdown).
type downloadImageInput struct {
	Path string `json:"path" jsonschema:"wiki-relative path to the image, e.g. projects/mind-map.assets/diagram.png"`
}

func (s *Server) downloadImage(ctx context.Context, _ *mcp.CallToolRequest, in downloadImageInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	data, mime, err := s.wiki.ReadAsset(ctx, in.Path)
	if err != nil {
		slog.Warn("tool.download_image failed",
			slog.String("path", in.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.download_image",
		slog.String("path", in.Path),
		slog.Int("bytes", len(data)),
		slog.String("mime", mime),
		slog.Duration("elapsed", time.Since(start)),
	)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.ImageContent{
				Data:     data,
				MIMEType: mime,
			},
		},
	}, nil, nil
}

// pageReadFlags carries the optional image-related read flags used by
// get_page and search_pages. Kept as a named type so the schema
// descriptions land on the same set of fields everywhere.
type pageReadFlags struct {
	IncludeImages         bool `json:"include_images,omitempty" jsonschema:"if true, include referenced images as MCP image content blocks alongside the page body. Default false — opt in only when a vision-capable agent needs to see the images inline. Server may force this off for token-constrained deployments."`
	IncludeImageMetadata  bool `json:"include_image_metadata,omitempty" jsonschema:"if true, include { path, size_bytes, mime } for each referenced image without the bytes. Cheap mode for non-vision agents or planning a follow-up download_image call. Default false."`
}

// getPageInput replaces the legacy pagePathInput for get_page so we can
// add the new flags without affecting other tools that still take a
// bare path.
type getPageInput struct {
	Path string `json:"path" jsonschema:"page path without .md extension, e.g. projects/mind-map"`
	pageReadFlags
}

// imageMetadata is the cheap-mode shape included in get_page responses
// when include_image_metadata=true. Subset of wiki.AssetInfo; we keep
// the JSON identical so callers parsing either source see the same
// fields.
type imageMetadata struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	MIME      string `json:"mime"`
	// Missing reports whether the file was missing on disk despite
	// being indexed. Lets agents distinguish "we couldn't read it"
	// from "it's a zero-byte file".
	Missing bool `json:"missing,omitempty"`
}

// pageWithImages is the JSON payload for include_image_metadata mode.
// We embed the wiki.Page directly so existing fields appear unchanged.
type pageWithImages struct {
	*wiki.Page
	Images []imageMetadata `json:"images,omitempty"`
	// ImagesForced, when true, signals that the operator-level kill
	// switch overrode the caller's request to include images or
	// image metadata. Agents should treat this as authoritative —
	// retrying with the flag set again won't help.
	ImagesForcedOff bool `json:"images_forced_off,omitempty"`
}

// getPageWithFlags is the new get_page handler. The signature change
// (from pagePathInput to getPageInput) is back-compat at the JSON
// level: the original `path` field is still required, the new fields
// are optional and default to false.
func (s *Server) getPageWithFlags(ctx context.Context, _ *mcp.CallToolRequest, in getPageInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	page, err := s.wiki.GetPage(ctx, in.Path)
	if err != nil {
		slog.Warn("tool.get_page failed", slog.String("page", in.Path), slog.Any("error", err))
		return nil, nil, err
	}

	wantMeta := in.IncludeImageMetadata
	wantBytes := in.IncludeImages
	forcedOff := s.forceImagesOff && (wantMeta || wantBytes)
	if s.forceImagesOff {
		wantMeta = false
		wantBytes = false
	}

	// Fast path: no image work requested → same response shape as
	// before. Keeps existing agents and tooling untouched.
	if !wantMeta && !wantBytes && !forcedOff {
		slog.Info("tool.get_page",
			slog.String("page", in.Path),
			slog.Duration("elapsed", time.Since(start)),
		)
		return textResult(page)
	}

	imageRefs, err := s.wiki.ImageRefsForPage(ctx, in.Path)
	if err != nil {
		slog.Warn("tool.get_page image refs failed", slog.String("page", in.Path), slog.Any("error", err))
		// Don't fail the call — the page body is still useful.
		imageRefs = nil
	}

	resp := pageWithImages{Page: page, ImagesForcedOff: forcedOff}

	if wantMeta {
		resp.Images = make([]imageMetadata, 0, len(imageRefs))
		for _, ref := range imageRefs {
			info, statErr := s.wiki.StatAsset(ctx, ref)
			if statErr != nil {
				resp.Images = append(resp.Images, imageMetadata{Path: ref, Missing: true})
				continue
			}
			resp.Images = append(resp.Images, imageMetadata{
				Path:      info.Path,
				SizeBytes: info.SizeBytes,
				MIME:      info.MIME,
			})
		}
	}

	// Build the multi-block response: JSON page body first (so non-
	// vision agents still get text), then optional image content
	// blocks for vision agents.
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	content := []mcp.Content{&mcp.TextContent{Text: string(data)}}

	if wantBytes {
		for _, ref := range imageRefs {
			body, mime, readErr := s.wiki.ReadAsset(ctx, ref)
			if readErr != nil {
				slog.Warn("tool.get_page include_images read failed",
					slog.String("asset", ref), slog.Any("error", readErr))
				continue
			}
			content = append(content, &mcp.ImageContent{
				Data:     body,
				MIMEType: mime,
			})
		}
	}

	slog.Info("tool.get_page",
		slog.String("page", in.Path),
		slog.Bool("include_images", wantBytes),
		slog.Bool("include_image_metadata", wantMeta),
		slog.Bool("forced_off", forcedOff),
		slog.Int("image_count", len(imageRefs)),
		slog.Duration("elapsed", time.Since(start)),
	)
	return &mcp.CallToolResult{Content: content}, nil, nil
}
