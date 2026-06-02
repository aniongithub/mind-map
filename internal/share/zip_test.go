package share

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// mockAssetReader implements AssetReader for tests.
type mockAssetReader struct {
	assets map[string][]byte
}

func (m *mockAssetReader) ReadAsset(_ context.Context, path string) ([]byte, string, error) {
	data, ok := m.assets[path]
	if !ok {
		return nil, "", io.EOF
	}
	return data, "image/png", nil
}

func TestZipSharer_Basic(t *testing.T) {
	z := &ZipSharer{}

	pages := []Page{
		{
			Path:        "projects/mind-map",
			Title:       "mind-map",
			Body:        "# mind-map\n\nA wiki engine.",
			Frontmatter: map[string]interface{}{"title": "mind-map", "type": "project"},
			ModifiedAt:  time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		},
		{
			Path:       "index",
			Title:      "Home",
			Body:       "# Welcome\n",
			ModifiedAt: time.Date(2025, 1, 14, 9, 0, 0, 0, time.UTC),
		},
	}

	cfg := ShareConfig{
		Format:   "zip",
		Settings: map[string]any{"include_assets": false},
	}
	req := ExportRequest{Config: cfg, Pages: pages}

	var buf bytes.Buffer
	if err := z.Export(context.Background(), &buf, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Read the zip and verify contents
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	if len(r.File) != 2 {
		t.Fatalf("expected 2 zip entries, got %d", len(r.File))
	}

	// Check first entry
	if r.File[0].Name != "index.md" && r.File[1].Name != "index.md" {
		t.Errorf("expected index.md in zip, got entries: %v, %v", r.File[0].Name, r.File[1].Name)
	}

	// Read content of first entry
	for _, f := range r.File {
		if f.Name == "projects/mind-map.md" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Open %s: %v", f.Name, err)
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			content := string(data)
			if !strings.Contains(content, "# mind-map") {
				t.Errorf("expected markdown body in %s, got: %s", f.Name, content)
			}
			if !strings.Contains(content, "title: mind-map") {
				t.Errorf("expected frontmatter in %s, got: %s", f.Name, content)
			}
		}
	}
}

func TestZipSharer_WithAssets(t *testing.T) {
	z := &ZipSharer{}

	pages := []Page{
		{
			Path:       "docs/guide",
			Title:      "Guide",
			Body:       "# Guide\n\n![diagram](docs/guide.assets/diagram.png)\n",
			ModifiedAt: time.Now(),
			ImageRefs:  []string{"docs/guide.assets/diagram.png"},
		},
	}

	assets := &mockAssetReader{
		assets: map[string][]byte{
			"docs/guide.assets/diagram.png": []byte("fake png data"),
		},
	}

	cfg := ShareConfig{
		Format:   "zip",
		Settings: map[string]any{"include_assets": true},
	}
	req := ExportRequest{Config: cfg, Pages: pages, Assets: assets}

	var buf bytes.Buffer
	if err := z.Export(context.Background(), &buf, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	// Should have the .md file and the asset
	if len(r.File) != 2 {
		names := make([]string, len(r.File))
		for i, f := range r.File {
			names[i] = f.Name
		}
		t.Fatalf("expected 2 zip entries, got %d: %v", len(r.File), names)
	}

	found := map[string]bool{}
	for _, f := range r.File {
		found[f.Name] = true
	}
	if !found["docs/guide.md"] {
		t.Error("missing docs/guide.md in zip")
	}
	if !found["docs/guide.assets/diagram.png"] {
		t.Error("missing docs/guide.assets/diagram.png in zip")
	}
}

func TestZipSharer_Flatten(t *testing.T) {
	z := &ZipSharer{}

	pages := []Page{
		{
			Path:       "projects/mind-map",
			Title:      "mind-map",
			Body:       "# mind-map\n",
			ModifiedAt: time.Now(),
		},
	}

	cfg := ShareConfig{
		Format:   "zip",
		Settings: map[string]any{"flatten": true, "include_assets": false},
	}
	req := ExportRequest{Config: cfg, Pages: pages}

	var buf bytes.Buffer
	if err := z.Export(context.Background(), &buf, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	if r.File[0].Name != "projects--mind-map.md" {
		t.Errorf("expected flattened name, got %q", r.File[0].Name)
	}
}

func TestRegistry(t *testing.T) {
	// The zip sharer is registered via init()
	s := Get("zip")
	if s == nil {
		t.Fatal("zip sharer not registered")
	}
	if s.Name() != "zip" {
		t.Errorf("expected name 'zip', got %q", s.Name())
	}
	if s.ContentType() != "application/zip" {
		t.Errorf("expected content type 'application/zip', got %q", s.ContentType())
	}
	if s.FileExtension() != ".zip" {
		t.Errorf("expected extension '.zip', got %q", s.FileExtension())
	}

	// Check settings schema
	settings := s.Settings()
	if len(settings.Fields) != 2 {
		t.Fatalf("expected 2 settings fields, got %d", len(settings.Fields))
	}
	if settings.Fields[0].Key != "include_assets" {
		t.Errorf("expected first field key 'include_assets', got %q", settings.Fields[0].Key)
	}
}

func TestFormats(t *testing.T) {
	formats := Formats()
	if len(formats) == 0 {
		t.Fatal("no formats registered")
	}
	found := false
	for _, f := range formats {
		if f.Name == "zip" {
			found = true
			break
		}
	}
	if !found {
		t.Error("zip format not found in Formats()")
	}
}

func TestSettingBool(t *testing.T) {
	cfg := ShareConfig{Settings: map[string]any{"flag": true}}
	if !SettingBool(cfg, "flag", false) {
		t.Error("expected true")
	}
	if SettingBool(cfg, "missing", true) != true {
		t.Error("expected default true for missing key")
	}
}

func TestSettingString(t *testing.T) {
	cfg := ShareConfig{Settings: map[string]any{"name": "hello"}}
	if SettingString(cfg, "name", "") != "hello" {
		t.Error("expected 'hello'")
	}
	if SettingString(cfg, "missing", "def") != "def" {
		t.Error("expected default 'def'")
	}
}
