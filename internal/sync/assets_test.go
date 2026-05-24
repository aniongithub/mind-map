package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aniongithub/mind-map/internal/config"
)

// TestSyncableRel covers the predicate the asset-aware sync uses to
// decide which files cross between the wiki and the shadow clone.
// Markdown is always carried; non-markdown files only travel when
// they live inside a *.assets/ sidecar directory.
func TestSyncableRel(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		// pages — any *.md travels
		{"index.md", true},
		{"projects/mm.md", true},
		{"projects/mm.scratch/notes.md", true},
		// sidecar assets
		{"projects/mm.assets/diagram.png", true},
		{"foo.assets/a.svg", true},
		// not ours
		{"random.txt", false},
		{"projects/mm.scratch/notes.txt", false},
		{"", false},
		// substring traps
		{"projects/notassets/x.png", false},
		{"projects/.assets-private/x.png", false},
	}
	for _, c := range cases {
		if got := syncableRel(c.rel); got != c.want {
			t.Errorf("syncableRel(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestSyncCarriesAssets exercises the end-to-end happy path: write a
// markdown file plus a sidecar asset on the wiki side, sync, and
// verify both ended up in the bare remote.
func TestSyncCarriesAssets(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath)

	wikiDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remotePath
	cfg.Sync.Interval = "5s"

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	// Drop a page + a sidecar asset on the wiki side.
	pageDir := filepath.Join(wikiDir, "projects")
	os.MkdirAll(filepath.Join(pageDir, "mm.assets"), 0o755)
	os.WriteFile(filepath.Join(pageDir, "mm.md"),
		[]byte("# mm\n\n![d](projects/mm.assets/d.png)\n"), 0o644)
	// onePixelPNG bytes inline so the test doesn't depend on cross-
	// package fixtures.
	png := []byte{
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
	os.WriteFile(filepath.Join(pageDir, "mm.assets", "d.png"), png, 0o644)

	// Force a sync cycle deterministically (the ticker is 5s; the
	// initial Start already runs one syncAll, but the wiki writes
	// landed after Start so we need a second pass). Easiest: trigger
	// Reload, which calls rebuildTargets but not sync; instead, just
	// call syncAll directly via the test helper.
	mgr.syncAll(context.Background())

	// Inspect the bare remote by cloning it into a scratch dir.
	scratch := t.TempDir()
	if out, err := exec.Command("git", "clone", remotePath, scratch).CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "projects/mm.md")); err != nil {
		t.Errorf("page not pushed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "projects/mm.assets/d.png")); err != nil {
		t.Errorf("asset not pushed: %v", err)
	}
}

// TestRegisterMappingWithOptionsLFS verifies that LFS settings round-
// trip through config and into the syncTarget. We don't push to a
// remote here — that requires git-lfs on the host and we want this
// test to run anywhere git is available.
func TestRegisterMappingWithOptionsLFS(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Interval = "5s"
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(t.TempDir(), cfgPath, cfg, &mockReindexer{})

	err := mgr.RegisterMappingWithOptions("projects/alpha", "https://example.com/alpha.git",
		MappingOptions{
			Direction: config.SyncBidirectional,
			LFS:       true,
		})
	if err != nil {
		t.Fatalf("RegisterMappingWithOptions: %v", err)
	}

	// Reload the config from disk and confirm it was persisted.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Sync.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(reloaded.Sync.Mappings))
	}
	m := reloaded.Sync.Mappings[0]
	if !m.LFS {
		t.Error("LFS not persisted to config")
	}
	if len(m.LFSPatterns) == 0 {
		t.Error("LFSPatterns not defaulted on persist")
	}
	if !strings.Contains(strings.Join(m.LFSPatterns, ","), "*.png") {
		t.Errorf("LFSPatterns missing *.png: %v", m.LFSPatterns)
	}

	// The in-memory target should also reflect LFS=true.
	mgr.mu.Lock()
	tgt, ok := mgr.targets["https://example.com/alpha.git"]
	mgr.mu.Unlock()
	if !ok {
		t.Fatal("target not registered")
	}
	if !tgt.lfs {
		t.Error("syncTarget.lfs not set")
	}
	if len(tgt.lfsPatterns) == 0 {
		t.Error("syncTarget.lfsPatterns not populated")
	}
}

// TestRegisterMappingBackCompatNoLFS verifies that the original
// RegisterMapping API still works and leaves LFS off.
func TestRegisterMappingBackCompatNoLFS(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)
	mgr := NewManager(t.TempDir(), cfgPath, cfg, &mockReindexer{})

	if err := mgr.RegisterMapping("p", "https://example.com/r.git", config.SyncPull); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := config.Load(cfgPath)
	if len(reloaded.Sync.Mappings) != 1 {
		t.Fatal("expected 1 mapping")
	}
	if reloaded.Sync.Mappings[0].LFS {
		t.Error("LFS should be false for RegisterMapping callers")
	}
}
