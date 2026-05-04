package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
)

// mockReindexer records reindex calls.
type mockReindexer struct {
	calls int
}

func (m *mockReindexer) Reindex(_ context.Context) error {
	m.calls++
	return nil
}

// setupBareRemote creates a bare git repo to act as the remote.
func setupBareRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s: %v", out, err)
	}
	return remote
}

// seedRemote pushes an initial commit to the bare remote.
func seedRemote(t *testing.T, remotePath string) {
	t.Helper()
	tmp := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
		}
	}
	run("init")
	run("checkout", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmp, "index.md"), []byte("# Home\n\nWelcome.\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "initial")
	run("remote", "add", "origin", remotePath)
	run("push", "-u", "origin", "main")
}

func TestSanitizeDirName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"https://github.com/user/repo.wiki.git", "github.com_user_repo.wiki"},
		{"git@github.com:user/repo.wiki.git", "github.com_user_repo.wiki"},
		{"https://github.com/org/project.wiki.git", "github.com_org_project.wiki"},
	}
	for _, tt := range tests {
		got := sanitizeDirName(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeDirName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestManagerSyncWithLocalRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath)

	wikiDir := t.TempDir()
	reindexer := &mockReindexer{}

	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remotePath
	cfg.Sync.Interval = "5s"

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, reindexer)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	// Initial sync should have pulled index.md
	content, err := os.ReadFile(filepath.Join(wikiDir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if !strings.Contains(string(content), "Welcome") {
		t.Errorf("index.md content = %q, expected 'Welcome'", content)
	}

	if reindexer.calls == 0 {
		t.Error("reindexer was not called")
	}

	// Create a local page and trigger sync
	os.WriteFile(filepath.Join(wikiDir, "notes.md"), []byte("# Notes\n\nSome notes.\n"), 0o644)
	mgr.syncAll(ctx)

	// Verify pushed to remote
	cloneTarget := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", remotePath, cloneTarget)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(cloneTarget, "notes.md")); err != nil {
		t.Error("notes.md was not pushed to remote")
	}
}

func TestManagerMultiRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remote1 := setupBareRemote(t)
	remote2 := setupBareRemote(t)

	wikiDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Interval = "5s"
	cfg.Sync.AddMapping("projects/alpha", remote1)
	cfg.Sync.AddMapping("projects/beta", remote2)

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	// Create pages under different prefixes
	os.MkdirAll(filepath.Join(wikiDir, "projects/alpha"), 0o755)
	os.MkdirAll(filepath.Join(wikiDir, "projects/beta"), 0o755)
	os.WriteFile(filepath.Join(wikiDir, "projects/alpha/design.md"), []byte("# Alpha Design\n"), 0o644)
	os.WriteFile(filepath.Join(wikiDir, "projects/beta/readme.md"), []byte("# Beta Readme\n"), 0o644)

	mgr.syncAll(ctx)

	// Verify alpha's page went to remote1
	clone1 := filepath.Join(t.TempDir(), "clone1")
	exec.Command("git", "clone", remote1, clone1).CombinedOutput()
	if _, err := os.Stat(filepath.Join(clone1, "design.md")); err != nil {
		t.Error("design.md not pushed to remote1")
	}
	// alpha should NOT have beta's page
	if _, err := os.Stat(filepath.Join(clone1, "readme.md")); err == nil {
		t.Error("readme.md should not be in remote1")
	}

	// Verify beta's page went to remote2
	clone2 := filepath.Join(t.TempDir(), "clone2")
	exec.Command("git", "clone", remote2, clone2).CombinedOutput()
	if _, err := os.Stat(filepath.Join(clone2, "readme.md")); err != nil {
		t.Error("readme.md not pushed to remote2")
	}
}

func TestRegisterMapping(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	wikiDir := t.TempDir()
	remote := setupBareRemote(t)

	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})

	// Register dynamically
	if err := mgr.RegisterMapping("projects/new", remote); err != nil {
		t.Fatalf("RegisterMapping: %v", err)
	}

	// Verify persisted to config
	loaded, _ := config.Load(cfgPath)
	if len(loaded.Sync.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(loaded.Sync.Mappings))
	}
	if loaded.Sync.Mappings[0].Prefix != "projects/new" {
		t.Errorf("prefix = %q", loaded.Sync.Mappings[0].Prefix)
	}

	// HasMapping should work
	if !mgr.HasMapping("projects/new/design") {
		t.Error("HasMapping should be true for projects/new/design")
	}
	if mgr.HasMapping("projects/other") {
		t.Error("HasMapping should be false for projects/other (no default)")
	}
}

func TestHasMappingWithDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Sync.Default = "https://github.com/user/wiki.wiki.git"

	mgr := NewManager("/tmp", "/tmp/cfg.json", cfg, nil)

	// Everything matches when there's a default
	if !mgr.HasMapping("anything/at/all") {
		t.Error("HasMapping should be true when default is set")
	}
}

func TestStartAndStop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remote := setupBareRemote(t)
	wikiDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remote
	cfg.Sync.Interval = "100ms"

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(350 * time.Millisecond)
	mgr.Stop()

	status := mgr.Status()
	if !status.Enabled {
		t.Error("status should show enabled")
	}
}
