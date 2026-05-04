package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestNewValidation(t *testing.T) {
	_, err := New(Config{Root: "/tmp", Remote: ""})
	if err == nil {
		t.Error("expected error for empty remote")
	}

	_, err = New(Config{Root: "", Remote: "https://example.com/repo.git"})
	if err == nil {
		t.Error("expected error for empty root")
	}
}

func TestAuthRemote(t *testing.T) {
	// No token -- return as-is
	g := &GitSync{remote: "https://github.com/user/repo.wiki.git"}
	if got := g.authRemote(); got != g.remote {
		t.Errorf("no token: got %q, want %q", got, g.remote)
	}

	// With token -- inject into HTTPS
	g.token = "ghp_abc123"
	got := g.authRemote()
	if !strings.Contains(got, "x-access-token:ghp_abc123@") {
		t.Errorf("with token: got %q, expected token injection", got)
	}

	// SSH remote -- don't inject
	g.remote = "git@github.com:user/repo.wiki.git"
	got = g.authRemote()
	if got != g.remote {
		t.Errorf("ssh remote: got %q, want unchanged", got)
	}
}

func TestEnsureGitignore(t *testing.T) {
	dir := t.TempDir()
	g := &GitSync{root: dir}
	g.ensureGitignore()

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(content), ".mind-map.db") {
		t.Error(".gitignore should contain .mind-map.db")
	}

	// Should not overwrite existing
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("custom\n"), 0o644)
	g.ensureGitignore()
	content, _ = os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(content) != "custom\n" {
		t.Error("should not overwrite existing .gitignore")
	}
}

func TestSyncWithLocalRemote(t *testing.T) {
	// Skip if git is not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	seedRemote(t, remotePath)

	wikiDir := t.TempDir()
	reindexer := &mockReindexer{}

	g, err := New(Config{
		Root:      wikiDir,
		Remote:    remotePath,
		Interval:  5 * time.Second,
		Reindexer: reindexer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()

	// Configure git user for commits
	gitConfig := func(key, value string) {
		cmd := exec.Command("git", "config", key, value)
		cmd.Dir = wikiDir
		cmd.Run()
	}

	if err := g.ensureGitRepo(ctx); err != nil {
		t.Fatalf("ensureGitRepo: %v", err)
	}

	gitConfig("user.email", "test@test.com")
	gitConfig("user.name", "Test")

	// Run a sync cycle
	g.syncOnce(ctx)

	status := g.Status()
	if status.LastError != "" {
		t.Fatalf("first sync error: %s", status.LastError)
	}

	// index.md should have been pulled from remote
	content, err := os.ReadFile(filepath.Join(wikiDir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if !strings.Contains(string(content), "Welcome") {
		t.Errorf("index.md content = %q, expected 'Welcome'", content)
	}

	// Reindexer should have been called
	if reindexer.calls == 0 {
		t.Error("reindexer was not called")
	}

	// Create a new local page and sync again
	os.WriteFile(filepath.Join(wikiDir, "notes.md"), []byte("# Notes\n\nSome notes.\n"), 0o644)
	g.syncOnce(ctx)

	status = g.Status()
	if status.LastError != "" {
		t.Fatalf("second sync error: %s", status.LastError)
	}

	// Verify the page was pushed to remote by cloning into a third dir
	cloneTarget := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", remotePath, cloneTarget)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone for verify: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(cloneTarget, "notes.md")); err != nil {
		entries, _ := os.ReadDir(cloneTarget)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("notes.md was not pushed to remote. Clone contains: %v", names)
	}
}

func TestStartAndStop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	remotePath := setupBareRemote(t)
	wikiDir := t.TempDir()

	g, err := New(Config{
		Root:      wikiDir,
		Remote:    remotePath,
		Interval:  100 * time.Millisecond,
		Reindexer: &mockReindexer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Configure git user
	ctx := context.Background()
	g.ensureGitRepo(ctx)
	cmd := exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = wikiDir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = wikiDir
	cmd.Run()

	if err := g.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let it run a couple cycles
	time.Sleep(350 * time.Millisecond)

	g.Stop()

	status := g.Status()
	if !status.LastSync.IsZero() {
		// It synced at least once
		t.Logf("last sync: %v", status.LastSync)
	}
}
