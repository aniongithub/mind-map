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
	"github.com/aniongithub/mind-map/internal/wiki"
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
	cfg.Sync.AddMapping("projects/alpha", remote1, config.SyncBidirectional)
	cfg.Sync.AddMapping("projects/beta", remote2, config.SyncBidirectional)

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
	if err := mgr.RegisterMapping("projects/new", remote, config.SyncBidirectional); err != nil {
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
	if loaded.Sync.Mappings[0].Direction != config.SyncBidirectional {
		t.Errorf("direction = %q, want bidirectional", loaded.Sync.Mappings[0].Direction)
	}

	// Re-registering with a different direction must update in place,
	// not append a duplicate. This mirrors the "pin direction" workflow
	// agents would use when switching a project to pull-only.
	if err := mgr.RegisterMapping("projects/new", remote, config.SyncPull); err != nil {
		t.Fatalf("RegisterMapping (direction change): %v", err)
	}
	loaded, _ = config.Load(cfgPath)
	if len(loaded.Sync.Mappings) != 1 {
		t.Fatalf("expected 1 mapping after re-register, got %d", len(loaded.Sync.Mappings))
	}
	if loaded.Sync.Mappings[0].Direction != config.SyncPull {
		t.Errorf("direction after re-register = %q, want pull", loaded.Sync.Mappings[0].Direction)
	}

	// Empty direction normalizes to bidirectional.
	if err := mgr.RegisterMapping("projects/another", remote, ""); err != nil {
		t.Fatalf("RegisterMapping (empty direction): %v", err)
	}
	loaded, _ = config.Load(cfgPath)
	var got config.SyncDirection
	for _, m := range loaded.Sync.Mappings {
		if m.Prefix == "projects/another" {
			got = m.Direction
		}
	}
	if got != config.SyncBidirectional {
		t.Errorf("empty direction did not normalize to bidirectional, got %q", got)
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

// TestLocalUpdateSurvivesBidirectionalSync exercises the bug where a local
// write that lands between two sync ticks is clobbered by the next pull
// because copyToWiki unconditionally overwrites every wiki file with its
// shadow-clone copy *before* copyFromWiki gets a chance to commit the
// local change. The local edit must (a) still be on disk after a sync
// cycle and (b) make it to the remote.
func TestLocalUpdateSurvivesBidirectionalSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath) // creates index.md with "Welcome"

	wikiDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remotePath
	cfg.Sync.Interval = "1h" // don't let the background ticker interfere
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	// After initial sync the seeded page is on disk.
	indexPath := filepath.Join(wikiDir, "index.md")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("initial sync did not populate wiki: %v", err)
	}

	// Simulate the user calling update_page: rewrite the local file
	// with new content. This is exactly what wiki.UpdatePage does.
	newContent := []byte("# Home\n\nUser made a local edit.\n")
	if err := os.WriteFile(indexPath, newContent, 0o644); err != nil {
		t.Fatalf("local update: %v", err)
	}

	// Next sync tick. The local edit must survive and propagate.
	mgr.syncAll(context.Background())

	got, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read after sync: %v", err)
	}
	if string(got) != string(newContent) {
		t.Errorf("local edit was clobbered by sync:\n  got:  %q\n  want: %q", got, newContent)
	}

	// And the remote must have received the edit.
	cloneTarget := filepath.Join(t.TempDir(), "verify")
	if out, err := exec.Command("git", "clone", remotePath, cloneTarget).CombinedOutput(); err != nil {
		t.Fatalf("clone for verify: %s: %v", out, err)
	}
	remoteContent, err := os.ReadFile(filepath.Join(cloneTarget, "index.md"))
	if err != nil {
		t.Fatalf("read remote index.md: %v", err)
	}
	if string(remoteContent) != string(newContent) {
		t.Errorf("remote did not receive local edit:\n  got:  %q\n  want: %q", remoteContent, newContent)
	}
}

// TestLocalDeleteSurvivesBidirectionalSync covers the matching delete_page
// scenario: a locally-deleted file must stay deleted and the deletion must
// propagate to the remote, rather than being undone by copyToWiki.
func TestLocalDeleteSurvivesBidirectionalSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath)

	wikiDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remotePath
	cfg.Sync.Interval = "1h"
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	indexPath := filepath.Join(wikiDir, "index.md")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("initial sync did not populate wiki: %v", err)
	}

	// Delete locally (what wiki.DeletePage does).
	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("local delete: %v", err)
	}

	mgr.syncAll(context.Background())

	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Errorf("local deletion was undone by sync (err=%v)", err)
	}

	cloneTarget := filepath.Join(t.TempDir(), "verify")
	if out, err := exec.Command("git", "clone", remotePath, cloneTarget).CombinedOutput(); err != nil {
		t.Fatalf("clone for verify: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(cloneTarget, "index.md")); !os.IsNotExist(err) {
		t.Errorf("remote did not receive deletion (err=%v)", err)
	}
}

// TestWikiUpdateAndDeleteThroughSync drives the same race as the two
// previous tests, but through the public wiki.Wiki API (UpdatePage,
// DeletePage) rather than raw os.WriteFile. This is closer to what the
// user actually reported: agent calls update_page / delete_page, the
// API returns success, but the next sync tick clobbers the change.
//
// The wiki here uses the same reindexer the manager calls, so the
// indexer state and on-disk state stay in sync the way they do in
// production.
func TestWikiUpdateAndDeleteThroughSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath)

	wikiDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Default = remotePath
	cfg.Sync.Interval = "1h"
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.Save(cfgPath, cfg)

	// First sync without a wiki to populate the wiki dir from the
	// seeded remote. Then open the wiki on the populated dir so its
	// index matches the on-disk state.
	bootstrap := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
	if err := bootstrap.Start(context.Background()); err != nil {
		t.Fatalf("bootstrap Start: %v", err)
	}
	bootstrap.Stop()

	w, err := wiki.Open(wikiDir)
	if err != nil {
		t.Fatalf("wiki.Open: %v", err)
	}
	defer w.Close()

	// Real reindexer wired to the wiki.
	mgr := NewManager(wikiDir, cfgPath, cfg, w)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	ctx := context.Background()

	// --- Update via the wiki API ---
	newBody := "# Home\n\nUpdated via the wiki API.\n"
	if err := w.UpdatePage(ctx, "index", newBody); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	mgr.syncAll(ctx)

	got, err := os.ReadFile(filepath.Join(wikiDir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if string(got) != newBody {
		t.Errorf("UpdatePage was clobbered by sync:\n  got:  %q\n  want: %q", got, newBody)
	}

	// The wiki's own index must still reflect the new body too.
	p, err := w.GetPage(ctx, "index")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if !strings.Contains(p.Body, "Updated via the wiki API") {
		t.Errorf("index page body does not contain new content: %q", p.Body)
	}

	// --- Delete via the wiki API ---
	if err := w.DeletePage(ctx, "index"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	mgr.syncAll(ctx)

	if _, err := os.Stat(filepath.Join(wikiDir, "index.md")); !os.IsNotExist(err) {
		t.Errorf("DeletePage was undone by sync (err=%v)", err)
	}
	if _, err := w.GetPage(ctx, "index"); err == nil {
		t.Error("wiki still indexes 'index' after delete+sync")
	}

	// Remote should reflect both operations: index.md is gone.
	cloneTarget := filepath.Join(t.TempDir(), "verify")
	if out, err := exec.Command("git", "clone", remotePath, cloneTarget).CombinedOutput(); err != nil {
		t.Fatalf("clone for verify: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(cloneTarget, "index.md")); !os.IsNotExist(err) {
		t.Errorf("remote still has index.md after wiki delete (err=%v)", err)
	}
}

func TestPullOnlyDoesNotPushLocalChanges(t *testing.T) {
if _, err := exec.LookPath("git"); err != nil {
t.Skip("git not found")
}

remotePath := setupBareRemote(t)
seedRemote(t, remotePath)

wikiDir := t.TempDir()
cfg := config.DefaultConfig()
cfg.Sync.Enabled = true
cfg.Sync.Interval = "5s"
cfg.Sync.Mappings = []config.SyncMapping{
{Prefix: "", Remote: remotePath, Direction: config.SyncPull},
}
cfgPath := filepath.Join(t.TempDir(), "config.json")
config.Save(cfgPath, cfg)

mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
if err := mgr.Start(context.Background()); err != nil {
t.Fatalf("Start: %v", err)
}
defer mgr.Stop()

// Pull happened: seeded index.md should be on disk.
if _, err := os.Stat(filepath.Join(wikiDir, "index.md")); err != nil {
t.Fatalf("pull did not populate wiki: %v", err)
}

// Drop a local-only page and run another sync cycle.
if err := os.WriteFile(filepath.Join(wikiDir, "local-only.md"), []byte("# Local\n"), 0o644); err != nil {
t.Fatalf("write local page: %v", err)
}
mgr.syncAll(context.Background())

// The local page must NOT have been pushed to the remote.
cloneTarget := filepath.Join(t.TempDir(), "verify")
cmd := exec.Command("git", "clone", remotePath, cloneTarget)
if out, err := cmd.CombinedOutput(); err != nil {
t.Fatalf("clone for verify: %s: %v", out, err)
}
if _, err := os.Stat(filepath.Join(cloneTarget, "local-only.md")); !os.IsNotExist(err) {
t.Errorf("pull-only mapping pushed local-only.md upstream (err=%v)", err)
}
}

func TestPushOnlyDoesNotPullRemoteChanges(t *testing.T) {
if _, err := exec.LookPath("git"); err != nil {
t.Skip("git not found")
}

remotePath := setupBareRemote(t)
seedRemote(t, remotePath) // creates remote index.md with "Welcome"

wikiDir := t.TempDir()
cfg := config.DefaultConfig()
cfg.Sync.Enabled = true
cfg.Sync.Interval = "5s"
cfg.Sync.Mappings = []config.SyncMapping{
{Prefix: "", Remote: remotePath, Direction: config.SyncPush},
}
cfgPath := filepath.Join(t.TempDir(), "config.json")
config.Save(cfgPath, cfg)

mgr := NewManager(wikiDir, cfgPath, cfg, &mockReindexer{})
if err := mgr.Start(context.Background()); err != nil {
t.Fatalf("Start: %v", err)
}
defer mgr.Stop()

// Remote had index.md; in push-only mode the wiki should NOT have
// gained it via pull.
if _, err := os.Stat(filepath.Join(wikiDir, "index.md")); !os.IsNotExist(err) {
t.Errorf("push-only mapping pulled index.md (err=%v)", err)
}

// Drop a local page and sync; it should be pushed.
if err := os.WriteFile(filepath.Join(wikiDir, "outbound.md"), []byte("# Out\n"), 0o644); err != nil {
t.Fatalf("write outbound page: %v", err)
}
mgr.syncAll(context.Background())

cloneTarget := filepath.Join(t.TempDir(), "verify")
cmd := exec.Command("git", "clone", remotePath, cloneTarget)
if out, err := cmd.CombinedOutput(); err != nil {
t.Fatalf("clone for verify: %s: %v", out, err)
}
if _, err := os.Stat(filepath.Join(cloneTarget, "outbound.md")); err != nil {
t.Errorf("push-only did not push outbound.md: %v", err)
}
}
