package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// onePixelPNG mirrors the test fixture from internal/wiki/assets_test.go.
// Duplicated here so the MCP tests don't need a cross-package import.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54,
	0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
	0xae, 0x42, 0x60, 0x82,
}

func TestUploadImageTool(t *testing.T) {
	session := setupTestServer(t)

	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	text := callTool(t, session, "upload_image", map[string]any{
		"page":           "projects/mind-map",
		"name":           "diagram.png",
		"content_base64": enc,
	})

	var out struct {
		Path      string `json:"path"`
		URL       string `json:"url"`
		SizeBytes int64  `json:"size_bytes"`
		MIME      string `json:"mime"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal upload_image: %v\n%s", err, text)
	}
	wantPath := "projects/mind-map.assets/diagram.png"
	if out.Path != wantPath {
		t.Errorf("path = %q, want %q", out.Path, wantPath)
	}
	if !strings.HasSuffix(out.URL, wantPath) {
		t.Errorf("URL = %q, want suffix %q", out.URL, wantPath)
	}
	if out.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", out.MIME)
	}
	if out.SizeBytes != int64(len(onePixelPNG)) {
		t.Errorf("SizeBytes = %d, want %d", out.SizeBytes, len(onePixelPNG))
	}
}

func TestDownloadImageTool(t *testing.T) {
	session := setupTestServer(t)
	ctx := context.Background()

	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	callTool(t, session, "upload_image", map[string]any{
		"page":           "projects/mind-map",
		"name":           "d.png",
		"content_base64": enc,
	})

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "download_image",
		Arguments: map[string]any{
			"path": "projects/mind-map.assets/d.png",
		},
	})
	if err != nil {
		t.Fatalf("CallTool download_image: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[0])
	}
	if img.MIMEType != "image/png" {
		t.Errorf("MIME = %q, want image/png", img.MIMEType)
	}
	// The Go SDK base64-encodes ImageContent.Data on the wire and
	// decodes back into []byte on the client side, so we can compare
	// directly here.
	if string(img.Data) != string(onePixelPNG) {
		t.Errorf("image bytes differ; got %d bytes, want %d", len(img.Data), len(onePixelPNG))
	}
}

func TestGetPageIncludeImageMetadata(t *testing.T) {
	session := setupTestServer(t)
	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	callTool(t, session, "upload_image", map[string]any{
		"page":           "projects/mind-map",
		"name":           "d.png",
		"content_base64": enc,
	})
	// Reference the asset from the page so the index picks it up.
	callTool(t, session, "update_page", map[string]any{
		"path":    "projects/mind-map",
		"content": "# mm\n\n![d](projects/mind-map.assets/d.png)\n",
	})

	text := callTool(t, session, "get_page", map[string]any{
		"path":                   "projects/mind-map",
		"include_image_metadata": true,
	})

	// Loose check: the JSON should mention the asset path and a size.
	if !strings.Contains(text, "projects/mind-map.assets/d.png") {
		t.Errorf("expected metadata to mention asset path, got: %s", text)
	}
	if !strings.Contains(text, "size_bytes") {
		t.Errorf("expected metadata to include size_bytes, got: %s", text)
	}
}

func TestGetPageIncludeImagesBytes(t *testing.T) {
	session := setupTestServer(t)
	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	callTool(t, session, "upload_image", map[string]any{
		"page":           "projects/mind-map",
		"name":           "d.png",
		"content_base64": enc,
	})
	callTool(t, session, "update_page", map[string]any{
		"path":    "projects/mind-map",
		"content": "# mm\n\n![d](projects/mind-map.assets/d.png)\n",
	})

	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_page",
		Arguments: map[string]any{
			"path":           "projects/mind-map",
			"include_images": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	// First block is the text payload; subsequent blocks are images.
	if len(result.Content) < 2 {
		t.Fatalf("expected text + at least one image, got %d blocks", len(result.Content))
	}
	if _, ok := result.Content[0].(*mcp.TextContent); !ok {
		t.Fatalf("first block = %T, want TextContent", result.Content[0])
	}
	gotImage := false
	for _, c := range result.Content[1:] {
		if img, ok := c.(*mcp.ImageContent); ok {
			gotImage = true
			if img.MIMEType != "image/png" {
				t.Errorf("image MIME = %q, want image/png", img.MIMEType)
			}
		}
	}
	if !gotImage {
		t.Errorf("no image content block in response")
	}
}

func TestUploadImageRejectsNonImage(t *testing.T) {
	session := setupTestServer(t)
	ctx := context.Background()
	enc := base64.StdEncoding.EncodeToString([]byte("not an image"))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "upload_image",
		Arguments: map[string]any{
			"page":           "projects/mind-map",
			"name":           "nope.png",
			"content_base64": enc,
		},
	})
	// The Go SDK surfaces handler errors as result.IsError rather
	// than as a transport error (the call itself succeeded; the tool
	// just reported failure). Both shapes are valid signals.
	if err == nil && !result.IsError {
		t.Fatal("upload_image accepted non-image content")
	}
}

func TestForceImagesOff(t *testing.T) {
	// Build a server with the kill-switch flipped on and verify the
	// flags are silently overridden.
	session, srv := setupTestServerForceOff(t)

	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	callTool(t, session, "upload_image", map[string]any{
		"page":           "projects/mind-map",
		"name":           "d.png",
		"content_base64": enc,
	})
	callTool(t, session, "update_page", map[string]any{
		"path":    "projects/mind-map",
		"content": "# mm\n\n![d](projects/mind-map.assets/d.png)\n",
	})

	text := callTool(t, session, "get_page", map[string]any{
		"path":           "projects/mind-map",
		"include_images": true,
	})
	if !strings.Contains(text, "\"images_forced_off\": true") {
		t.Errorf("expected images_forced_off in response when force-off is set:\n%s", text)
	}
	_ = srv
}

// setupTestServerForceOff wires a server with force-images-off enabled.
// Mirrors setupTestServer but exposes the *Server too so tests can poke
// at server-level flags.
func setupTestServerForceOff(t *testing.T) (*mcp.ClientSession, *Server) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, "index.md", "# Home\n")
	writeTestFile(t, dir, "projects/mind-map.md", "# mm\n")
	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("Open wiki: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	s := NewServer(w, nil, "test")
	s.SetForceImagesOff(true)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCPServer().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session, s
}
