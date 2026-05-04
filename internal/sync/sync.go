// Package sync provides git-based wiki synchronization.
// It pulls from and pushes to a remote git repository on an interval,
// keeping wiki pages synchronized across machines.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Reindexer is the interface the sync engine uses to trigger a wiki reindex
// after pulling changes.
type Reindexer interface {
	Reindex(ctx context.Context) error
}

// Status represents the current sync state.
type Status struct {
	Enabled      bool      `json:"enabled"`
	Remote       string    `json:"remote"`
	LastSync     time.Time `json:"last_sync,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	Conflicts    []string  `json:"conflicts,omitempty"`
	PendingFiles int       `json:"pending_files"`
}

// GitSync manages bidirectional sync between a wiki directory and a git remote.
type GitSync struct {
	root      string
	remote    string
	token     string
	interval  time.Duration
	reindexer Reindexer

	mu        sync.Mutex
	status    Status
	cancel    context.CancelFunc
	done      chan struct{}
}

// Config holds the parameters for creating a GitSync.
type Config struct {
	Root      string
	Remote    string
	Token     string
	Interval  time.Duration
	Reindexer Reindexer
}

// New creates a GitSync but does not start it.
func New(cfg Config) (*GitSync, error) {
	if cfg.Remote == "" {
		return nil, errors.New("sync remote is required")
	}
	if cfg.Root == "" {
		return nil, errors.New("sync root is required")
	}
	if cfg.Interval < 5*time.Second {
		cfg.Interval = 30 * time.Second
	}

	return &GitSync{
		root:      cfg.Root,
		remote:    cfg.Remote,
		token:     cfg.Token,
		interval:  cfg.Interval,
		reindexer: cfg.Reindexer,
		status: Status{
			Enabled: true,
			Remote:  cfg.Remote,
		},
	}, nil
}

// Start begins the background sync loop.
func (g *GitSync) Start(ctx context.Context) error {
	if err := g.ensureGitRepo(ctx); err != nil {
		return fmt.Errorf("git init: %w", err)
	}

	ctx, g.cancel = context.WithCancel(ctx)
	g.done = make(chan struct{})

	// Do an initial sync immediately
	g.syncOnce(ctx)

	go func() {
		defer close(g.done)
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.syncOnce(ctx)
			}
		}
	}()

	slog.Info("sync started", slog.String("remote", g.remote), slog.Duration("interval", g.interval))
	return nil
}

// Stop halts the background sync loop and waits for it to finish.
func (g *GitSync) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
	if g.done != nil {
		<-g.done
	}
	slog.Info("sync stopped")
}

// Status returns the current sync status.
func (g *GitSync) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}

// syncOnce performs a single pull-commit-push cycle.
func (g *GitSync) syncOnce(ctx context.Context) {
	g.mu.Lock()
	g.status.LastError = ""
	g.mu.Unlock()

	// Ensure .gitignore exists
	g.ensureGitignore()

	// Pull remote changes
	if err := g.pull(ctx); err != nil {
		g.setError(fmt.Sprintf("pull: %v", err))
		return
	}

	// Check for conflicts after pull
	conflicts := g.checkConflicts(ctx)

	// Reindex after pull to pick up remote changes
	if g.reindexer != nil {
		if err := g.reindexer.Reindex(ctx); err != nil {
			slog.Warn("reindex after pull failed", slog.Any("error", err))
		}
	}

	// Stage and commit local changes
	if err := g.commitAll(ctx); err != nil {
		g.setError(fmt.Sprintf("commit: %v", err))
		return
	}

	// Push
	if err := g.push(ctx); err != nil {
		g.setError(fmt.Sprintf("push: %v", err))
		return
	}

	g.mu.Lock()
	g.status.LastSync = time.Now()
	g.status.Conflicts = conflicts
	g.status.LastError = ""
	g.mu.Unlock()

	if len(conflicts) > 0 {
		slog.Warn("sync complete with conflicts", slog.Int("conflicts", len(conflicts)))
	} else {
		slog.Debug("sync complete")
	}
}

// ensureGitRepo initializes the wiki directory as a git repo if needed,
// and configures the remote.
func (g *GitSync) ensureGitRepo(ctx context.Context) error {
	gitDir := filepath.Join(g.root, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		slog.Info("initializing git repo for sync", slog.String("root", g.root))
		if err := g.git(ctx, "init"); err != nil {
			return err
		}
		// Set default branch to main
		if err := g.git(ctx, "checkout", "-b", "main"); err != nil {
			// May already be on main
			slog.Debug("checkout main failed (may already exist)", slog.Any("error", err))
		}
	}

	// Configure remote
	remote := g.authRemote()
	_ = g.git(ctx, "remote", "remove", "origin")
	if err := g.git(ctx, "remote", "add", "origin", remote); err != nil {
		return fmt.Errorf("add remote: %w", err)
	}

	return nil
}

// ensureGitignore creates a .gitignore if it doesn't exist,
// ignoring the SQLite database and other transient files.
func (g *GitSync) ensureGitignore() {
	path := filepath.Join(g.root, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return
	}
	content := ".mind-map.db\n.mind-map.db-wal\n.mind-map.db-shm\n"
	os.WriteFile(path, []byte(content), 0o644)
}

// pull fetches and merges from the remote.
func (g *GitSync) pull(ctx context.Context) error {
	// Check if remote has any commits first
	err := g.git(ctx, "fetch", "origin")
	if err != nil {
		return err
	}

	// Check if the remote branch exists
	err = g.git(ctx, "rev-parse", "--verify", "origin/main")
	if err != nil {
		// Remote has no commits yet, skip pull
		slog.Debug("remote branch not found, skipping pull")
		return nil
	}

	// Try to merge
	err = g.git(ctx, "merge", "origin/main", "--allow-unrelated-histories", "--no-edit")
	if err != nil {
		// Merge conflict -- leave markers in files for user resolution
		slog.Warn("merge conflict detected", slog.Any("error", err))
	}
	return nil
}

// commitAll stages all changes and commits if there are any.
func (g *GitSync) commitAll(ctx context.Context) error {
	if err := g.git(ctx, "add", "-A"); err != nil {
		return err
	}

	// Check if there's anything to commit
	err := g.git(ctx, "diff", "--cached", "--quiet")
	if err == nil {
		// No changes staged
		return nil
	}

	hostname, _ := os.Hostname()
	msg := fmt.Sprintf("sync from %s at %s", hostname, time.Now().UTC().Format(time.RFC3339))
	return g.git(ctx, "commit", "-m", msg)
}

// push pushes commits to the remote.
func (g *GitSync) push(ctx context.Context) error {
	// Check if we have any commits
	err := g.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		// No commits yet
		return nil
	}

	return g.git(ctx, "push", "-u", "origin", "main")
}

// checkConflicts looks for merge conflict markers in tracked files.
func (g *GitSync) checkConflicts(ctx context.Context) []string {
	out, err := g.gitOutput(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}

	var conflicts []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasSuffix(line, ".md") {
			conflicts = append(conflicts, strings.TrimSuffix(line, ".md"))
		}
	}
	return conflicts
}

// authRemote returns the remote URL with token injected if needed.
func (g *GitSync) authRemote() string {
	if g.token == "" {
		return g.remote
	}

	// Only inject token for HTTPS URLs
	u, err := url.Parse(g.remote)
	if err != nil || u.Scheme != "https" {
		return g.remote
	}

	u.User = url.UserPassword("x-access-token", g.token)
	return u.String()
}

// git runs a git command in the wiki directory.
func (g *GitSync) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// gitOutput runs a git command and returns stdout.
func (g *GitSync) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (g *GitSync) setError(msg string) {
	slog.Warn("sync error", slog.String("error", msg))
	g.mu.Lock()
	g.status.LastError = msg
	g.mu.Unlock()
}
