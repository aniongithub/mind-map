package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// onePixelPNG mirrors the wiki package's fixture; duplicated to keep
// the httpapi tests free of cross-package internal imports.
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

func TestUploadAssetJSON(t *testing.T) {
	h := newTestServer(t)

	// The temporary wiki has no pages, so create one first.
	doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "projects/mind-map",
		"content": "# mm\n",
	})

	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	rec := doJSON(t, h, "POST", "/api/assets", map[string]string{
		"page":           "projects/mind-map",
		"name":           "diagram.png",
		"content_base64": enc,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp uploadAssetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Path != "projects/mind-map.assets/diagram.png" {
		t.Errorf("Path = %q", resp.Path)
	}
	if resp.URL != "/assets/projects/mind-map.assets/diagram.png" {
		t.Errorf("URL = %q", resp.URL)
	}
	if resp.MIME != "image/png" {
		t.Errorf("MIME = %q", resp.MIME)
	}
	if resp.SizeBytes != int64(len(onePixelPNG)) {
		t.Errorf("SizeBytes = %d, want %d", resp.SizeBytes, len(onePixelPNG))
	}
}

func TestUploadAssetMultipart(t *testing.T) {
	h := newTestServer(t)
	doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "projects/mind-map",
		"content": "# mm\n",
	})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("page", "projects/mind-map")
	_ = writer.WriteField("name", "shot.png")
	part, err := writer.CreateFormFile("file", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(onePixelPNG); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/assets", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp uploadAssetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasSuffix(resp.Path, "shot.png") {
		t.Errorf("Path = %q", resp.Path)
	}
}

func TestUploadAssetRejectsNonImage(t *testing.T) {
	h := newTestServer(t)
	doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "projects/mind-map",
		"content": "# mm\n",
	})

	enc := base64.StdEncoding.EncodeToString([]byte("definitely not a png"))
	rec := doJSON(t, h, "POST", "/api/assets", map[string]string{
		"page":           "projects/mind-map",
		"name":           "evil.png",
		"content_base64": enc,
	})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415; body=%s", rec.Code, rec.Body.String())
	}
}

func TestServeAsset(t *testing.T) {
	h := newTestServer(t)
	doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "projects/mind-map",
		"content": "# mm\n",
	})
	enc := base64.StdEncoding.EncodeToString(onePixelPNG)
	doJSON(t, h, "POST", "/api/assets", map[string]string{
		"page":           "projects/mind-map",
		"name":           "d.png",
		"content_base64": enc,
	})

	req := httptest.NewRequest("GET", "/assets/projects/mind-map.assets/d.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	body, _ := io.ReadAll(rec.Body)
	if !bytes.Equal(body, onePixelPNG) {
		t.Errorf("body differs from uploaded PNG")
	}
}

func TestServeAssetSVGCSP(t *testing.T) {
	h := newTestServer(t)
	doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "projects/mind-map",
		"content": "# mm\n",
	})
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect/></svg>`
	enc := base64.StdEncoding.EncodeToString([]byte(svg))
	doJSON(t, h, "POST", "/api/assets", map[string]string{
		"page":           "projects/mind-map",
		"name":           "vec.svg",
		"content_base64": enc,
	})

	req := httptest.NewRequest("GET", "/assets/projects/mind-map.assets/vec.svg", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("SVG response missing Content-Security-Policy header")
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, expected restrictive default-src 'none'", csp)
	}
}

func TestServeAssetNotFound(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest("GET", "/assets/does/not/exist.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestServeAssetRejectsTraversal(t *testing.T) {
	h := newTestServer(t)
	// The Go 1.22 routing pattern auto-cleans paths, so "../" usually
	// gets stripped before reaching the handler. We still want to
	// confirm the wiki-layer guard returns a 4xx for any path that
	// somehow makes it through.
	req := httptest.NewRequest("GET", "/assets/..%2F..%2Fetc%2Fpasswd", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("traversal accepted: status %d", rec.Code)
	}
}
