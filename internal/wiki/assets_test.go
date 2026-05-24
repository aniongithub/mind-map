package wiki

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- test fixtures ---

// onePixelPNG is the smallest possible valid PNG: 1x1 transparent.
// Generated once by hand; the magic bytes and IDAT are what http.DetectContentType
// inspects, so any browser-renderable PNG works in tests.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // signature
	0x00, 0x00, 0x00, 0x0d, // IHDR length
	0x49, 0x48, 0x44, 0x52, // "IHDR"
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
	0x08, 0x06, 0x00, 0x00, 0x00, // bit depth + color
	0x1f, 0x15, 0xc4, 0x89, // CRC
	0x00, 0x00, 0x00, 0x0d, // IDAT length
	0x49, 0x44, 0x41, 0x54, // "IDAT"
	0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, // data + CRC
	0x00, 0x00, 0x00, 0x00, // IEND length
	0x49, 0x45, 0x4e, 0x44, // "IEND"
	0xae, 0x42, 0x60, 0x82, // CRC
}

const tinySVG = `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect/></svg>`

// --- UploadAsset ---

func TestUploadAssetCreatesSidecar(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	got, err := w.UploadAsset(ctx, "projects/mind-map", "diagram.png", onePixelPNG)
	if err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	want := "projects/mind-map.assets/diagram.png"
	if got != want {
		t.Errorf("returned path = %q, want %q", got, want)
	}
	abs := filepath.Join(dir, filepath.FromSlash(got))
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("asset file missing on disk: %v", err)
	}
}

func TestUploadAssetCollisionSuffix(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	first, err := w.UploadAsset(ctx, "projects/mind-map", "shot.png", onePixelPNG)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second, err := w.UploadAsset(ctx, "projects/mind-map", "shot.png", onePixelPNG)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if first == second {
		t.Fatalf("expected collision suffix, both uploads returned %q", first)
	}
	if !strings.HasSuffix(second, "shot-1.png") {
		t.Errorf("second upload path %q does not end with shot-1.png", second)
	}

	// Case-insensitive collision: SHOT.PNG should also bump.
	third, err := w.UploadAsset(ctx, "projects/mind-map", "SHOT.PNG", onePixelPNG)
	if err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if strings.EqualFold(filepath.Base(third), "shot.png") || strings.EqualFold(filepath.Base(third), "shot-1.png") {
		t.Errorf("third upload %q collided case-insensitively", third)
	}
}

func TestUploadAssetRejectsNonImage(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	_, err := w.UploadAsset(ctx, "projects/mind-map", "evil.exe",
		[]byte("MZ\x00\x00not actually a PE but http.DetectContentType picks it up"))
	if err == nil {
		t.Fatal("UploadAsset accepted non-image content")
	}
	if !errors.Is(err, ErrUnsupportedAssetType) {
		t.Errorf("err = %v, want ErrUnsupportedAssetType", err)
	}
}

func TestUploadAssetAcceptsSVG(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	got, err := w.UploadAsset(ctx, "projects/mind-map", "vector.svg", []byte(tinySVG))
	if err != nil {
		t.Fatalf("UploadAsset svg: %v", err)
	}
	if !strings.HasSuffix(got, ".svg") {
		t.Errorf("returned path = %q, want .svg suffix", got)
	}
}

func TestUploadAssetSizeCap(t *testing.T) {
	w, _ := testWiki(t)
	w.MaxAssetBytes = 64 // tiny cap for the test
	ctx := context.Background()

	big := bytes.Repeat([]byte{0}, 256) // not even an image, but we want size to fail first
	_, err := w.UploadAsset(ctx, "projects/mind-map", "big.png", big)
	if err == nil || !errors.Is(err, ErrAssetTooLarge) {
		t.Errorf("err = %v, want ErrAssetTooLarge", err)
	}
}

func TestUploadAssetSanitizesFilename(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	got, err := w.UploadAsset(ctx, "projects/mind-map", "../../evil.png", onePixelPNG)
	if err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	if strings.Contains(got, "..") {
		t.Errorf("returned path contains traversal: %q", got)
	}
	if filepath.Base(got) != "evil.png" {
		t.Errorf("expected basename evil.png, got %q", got)
	}
}

// --- ReadAsset / StatAsset ---

func TestReadAssetRoundTrip(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	upPath, err := w.UploadAsset(ctx, "projects/mind-map", "d.png", onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	bs, mime, err := w.ReadAsset(ctx, upPath)
	if err != nil {
		t.Fatalf("ReadAsset: %v", err)
	}
	if !bytes.Equal(bs, onePixelPNG) {
		t.Error("bytes differ from uploaded content")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

func TestReadAssetRejectsTraversal(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	_, _, err := w.ReadAsset(ctx, "../../../etc/passwd")
	if err == nil {
		t.Fatal("ReadAsset accepted traversal path")
	}
}

func TestStatAsset(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	upPath, _ := w.UploadAsset(ctx, "projects/mind-map", "d.png", onePixelPNG)
	info, err := w.StatAsset(ctx, upPath)
	if err != nil {
		t.Fatalf("StatAsset: %v", err)
	}
	if info.SizeBytes != int64(len(onePixelPNG)) {
		t.Errorf("SizeBytes = %d, want %d", info.SizeBytes, len(onePixelPNG))
	}
	if info.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", info.MIME)
	}
	if info.Path != upPath {
		t.Errorf("Path = %q, want %q", info.Path, upPath)
	}
}

// --- DeletePage cascade ---

func TestDeletePageCascadesUnreferencedAssets(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	uploaded, err := w.UploadAsset(ctx, "projects/mind-map", "d.png", onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	// Update the page so the index knows it references the asset.
	if err := w.UpdatePage(ctx, "projects/mind-map", "# mm\n\n![d]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}

	if err := w.DeletePage(ctx, "projects/mind-map"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	// Asset should be gone.
	abs := filepath.Join(dir, filepath.FromSlash(uploaded))
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("asset still exists after delete: %v", err)
	}
	// Sidecar dir should be gone too.
	sidecar := filepath.Join(dir, "projects/mind-map.assets")
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("sidecar dir still exists after delete: %v", err)
	}
}

func TestDeletePageKeepsSharedAssets(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	uploaded, err := w.UploadAsset(ctx, "projects/mind-map", "d.png", onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.UpdatePage(ctx, "projects/mind-map", "# mm\n\n![d]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}
	// people/alice also references the same asset.
	if err := w.UpdatePage(ctx, "people/alice", "# Alice\n\n![d]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}

	if err := w.DeletePage(ctx, "projects/mind-map"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	abs := filepath.Join(dir, filepath.FromSlash(uploaded))
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("shared asset removed despite external referencer: %v", err)
	}
}

// --- MovePage with sidecar ---

func TestMovePageRelocatesExclusiveAssets(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	uploaded, _ := w.UploadAsset(ctx, "projects/mind-map", "d.png", onePixelPNG)
	if err := w.UpdatePage(ctx, "projects/mind-map", "# mm\n\n![d]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}

	if err := w.MovePage(ctx, "projects/mind-map", "projects/mm2", MoveOptions{}); err != nil {
		t.Fatalf("MovePage: %v", err)
	}

	// Old asset should be gone, new one in place at the new sidecar.
	if _, err := os.Stat(filepath.Join(dir, "projects/mind-map.assets/d.png")); !os.IsNotExist(err) {
		t.Errorf("old asset file still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projects/mm2.assets/d.png")); err != nil {
		t.Errorf("new asset file missing: %v", err)
	}

	// Page body should have the rewritten reference.
	p, err := w.GetPage(ctx, "projects/mm2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "projects/mm2.assets/d.png") {
		t.Errorf("body not rewritten: %q", p.Body)
	}
	if strings.Contains(p.Body, "projects/mind-map.assets/") {
		t.Errorf("old sidecar path still in body: %q", p.Body)
	}
}

func TestMovePageLeavesSharedAssetsBehind(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	uploaded, _ := w.UploadAsset(ctx, "projects/mind-map", "shared.png", onePixelPNG)
	// Both pages reference the asset that lives in projects/mind-map.assets/.
	if err := w.UpdatePage(ctx, "projects/mind-map", "# mm\n\n![s]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.UpdatePage(ctx, "people/alice", "# Alice\n\n![s]("+uploaded+")\n"); err != nil {
		t.Fatal(err)
	}

	if err := w.MovePage(ctx, "projects/mind-map", "projects/mm2", MoveOptions{}); err != nil {
		t.Fatalf("MovePage: %v", err)
	}

	// The shared file must stay in its original sidecar.
	if _, err := os.Stat(filepath.Join(dir, "projects/mind-map.assets/shared.png")); err != nil {
		t.Errorf("shared asset removed from original sidecar: %v", err)
	}

	// The moved page's body should keep referencing the old sidecar
	// path, because the file still lives there.
	p, err := w.GetPage(ctx, "projects/mm2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "projects/mind-map.assets/shared.png") {
		t.Errorf("moved page body lost reference to shared asset: %q", p.Body)
	}

	// And alice still resolves.
	a, _ := w.GetPage(ctx, "people/alice")
	if !strings.Contains(a.Body, "projects/mind-map.assets/shared.png") {
		t.Errorf("alice lost her reference: %q", a.Body)
	}
}
