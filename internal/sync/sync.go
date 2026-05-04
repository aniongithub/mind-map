// Package sync provides git-based wiki synchronization.
// It pulls from and pushes to remote git repositories on an interval,
// keeping wiki pages synchronized across machines. Each prefix-to-remote
// mapping gets its own shadow clone, so multiple repos can sync independently.
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
)

// Reindexer is the interface the sync engine uses to trigger a wiki reindex
// after pulling changes.
type Reindexer interface {
	Reindex(ctx context.Context) error
}

// RemoteStatus represents the sync state for a single remote.
type RemoteStatus struct {
	Remote    string   `json:"remote"`
	Prefix    string   `json:"prefix"`
	LastSync  string   `json:"last_sync,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Status represents the overall sync state.
type Status struct {
	Enabled bool           `json:"enabled"`
	Remotes []RemoteStatus `json:"remotes,omitempty"`
}

// Manager manages multiple sync targets, one per unique remote.
type Manager struct {
	wikiRoot  string
	syncDir   string // ~/.mind-map/sync/
	cfg       *config.Config
	cfgPath   string
	reindexer Reindexer
	interval  time.Duration

	mu      sync.Mutex
	targets map[string]*syncTarget // remote URL -> target
	cancel  context.CancelFunc
	done    chan struct{}
}

// syncTarget manages a single shadow clone for one remote.
type syncTarget struct {
	remote   string
	cloneDir string
	prefixes []string // wiki prefixes that map to this remote

	mu        sync.Mutex
	lastSync  time.Time
	lastError string
	conflicts []string
}

// NewManager creates a sync manager.
func NewManager(wikiRoot, cfgPath string, cfg *config.Config, reindexer Reindexer) *Manager {
	home, _ := os.UserHomeDir()
	syncDir := filepath.Join(home, ".mind-map", "sync")

	return &Manager{
		wikiRoot:  wikiRoot,
		syncDir:   syncDir,
		cfg:       cfg,
		cfgPath:   cfgPath,
		reindexer: reindexer,
		interval:  cfg.Sync.ParseInterval(),
		targets:   make(map[string]*syncTarget),
	}
}

// Start begins the background sync loop for all configured remotes.
func (m *Manager) Start(ctx context.Context) error {
	if err := os.MkdirAll(m.syncDir, 0o755); err != nil {
		return fmt.Errorf("create sync dir: %w", err)
	}

	m.rebuildTargets()

	ctx, m.cancel = context.WithCancel(ctx)
	m.done = make(chan struct{})

	// Initial sync
	m.syncAll(ctx)

	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.syncAll(ctx)
			}
		}
	}()

	slog.Info("sync manager started", slog.Int("targets", len(m.targets)), slog.Duration("interval", m.interval))
	return nil
}

// Stop halts the background sync loop.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		<-m.done
	}
	slog.Info("sync manager stopped")
}

// RegisterMapping adds a prefix-to-remote mapping, saves config, and
// sets up the sync target. Returns immediately; sync happens on next cycle.
func (m *Manager) RegisterMapping(prefix, remote string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg.Sync.AddMapping(prefix, remote)
	if err := config.Save(m.cfgPath, m.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	m.rebuildTargetsLocked()
	slog.Info("sync mapping registered", slog.String("prefix", prefix), slog.String("remote", remote))
	return nil
}

// HasMapping returns true if the given page path has a sync mapping
// (either explicit or default).
func (m *Manager) HasMapping(pagePath string) bool {
	return m.cfg.Sync.ResolveRemote(pagePath) != ""
}

// Status returns the current sync status for all targets.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := Status{Enabled: m.cfg.Sync.Enabled}
	for _, t := range m.targets {
		t.mu.Lock()
		rs := RemoteStatus{
			Remote:    t.remote,
			Prefix:    strings.Join(t.prefixes, ", "),
			Conflicts: t.conflicts,
		}
		if !t.lastSync.IsZero() {
			rs.LastSync = t.lastSync.Format(time.RFC3339)
		}
		rs.LastError = t.lastError
		t.mu.Unlock()
		s.Remotes = append(s.Remotes, rs)
	}
	return s
}

// rebuildTargets rebuilds the target map from config. Caller must NOT hold m.mu.
func (m *Manager) rebuildTargets() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildTargetsLocked()
}

// rebuildTargetsLocked rebuilds targets. Caller must hold m.mu.
func (m *Manager) rebuildTargetsLocked() {
	// Build remote -> prefixes map
	remotePrefixes := make(map[string][]string)
	if m.cfg.Sync.Default != "" {
		remotePrefixes[m.cfg.Sync.Default] = append(remotePrefixes[m.cfg.Sync.Default], "")
	}
	for _, mapping := range m.cfg.Sync.Mappings {
		if mapping.Remote != "" {
			remotePrefixes[mapping.Remote] = append(remotePrefixes[mapping.Remote], mapping.Prefix)
		}
	}

	// Create or update targets
	for remote, prefixes := range remotePrefixes {
		if t, exists := m.targets[remote]; exists {
			t.prefixes = prefixes
		} else {
			dirName := sanitizeDirName(remote)
			m.targets[remote] = &syncTarget{
				remote:   remote,
				cloneDir: filepath.Join(m.syncDir, dirName),
				prefixes: prefixes,
			}
		}
	}

	// Remove targets no longer in config
	for remote := range m.targets {
		if _, exists := remotePrefixes[remote]; !exists {
			delete(m.targets, remote)
		}
	}
}

// syncAll syncs all targets.
func (m *Manager) syncAll(ctx context.Context) {
	m.mu.Lock()
	targets := make([]*syncTarget, 0, len(m.targets))
	for _, t := range m.targets {
		targets = append(targets, t)
	}
	m.mu.Unlock()

	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		m.syncTarget(ctx, t)
	}
}

// syncTarget syncs a single remote: pull -> copy in -> reindex -> copy out -> commit -> push.
func (m *Manager) syncTarget(ctx context.Context, t *syncTarget) {
	t.mu.Lock()
	t.lastError = ""
	t.mu.Unlock()

	// Ensure clone exists
	if err := m.ensureClone(ctx, t); err != nil {
		t.setError(fmt.Sprintf("clone: %v", err))
		return
	}

	// Pull
	if err := gitCmd(ctx, t.cloneDir, "fetch", "origin"); err != nil {
		t.setError(fmt.Sprintf("fetch: %v", err))
		return
	}

	// Check if remote branch exists
	if err := gitCmd(ctx, t.cloneDir, "rev-parse", "--verify", "origin/main"); err == nil {
		if err := gitCmd(ctx, t.cloneDir, "merge", "origin/main", "--allow-unrelated-histories", "--no-edit"); err != nil {
			slog.Warn("merge conflict", slog.String("remote", t.remote), slog.Any("error", err))
		}
	}

	// Copy from clone to wiki (pull direction)
	m.copyToWiki(t)

	// Reindex to pick up pulled changes
	if m.reindexer != nil {
		if err := m.reindexer.Reindex(ctx); err != nil {
			slog.Warn("reindex after pull failed", slog.Any("error", err))
		}
	}

	// Copy from wiki to clone (push direction)
	m.copyFromWiki(t)

	// Check for conflicts
	conflicts := checkConflicts(ctx, t.cloneDir)

	// Commit and push
	ensureGitignore(t.cloneDir)
	if err := gitCmd(ctx, t.cloneDir, "add", "-A"); err != nil {
		t.setError(fmt.Sprintf("add: %v", err))
		return
	}

	// Only commit if there are staged changes
	if err := gitCmd(ctx, t.cloneDir, "diff", "--cached", "--quiet"); err != nil {
		hostname, _ := os.Hostname()
		msg := fmt.Sprintf("sync from %s at %s", hostname, time.Now().UTC().Format(time.RFC3339))
		if err := gitCmd(ctx, t.cloneDir, "commit", "-m", msg); err != nil {
			t.setError(fmt.Sprintf("commit: %v", err))
			return
		}
	}

	// Push (only if we have commits)
	if err := gitCmd(ctx, t.cloneDir, "rev-parse", "HEAD"); err == nil {
		if err := gitCmd(ctx, t.cloneDir, "push", "-u", "origin", "main"); err != nil {
			t.setError(fmt.Sprintf("push: %v", err))
			return
		}
	}

	t.mu.Lock()
	t.lastSync = time.Now()
	t.lastError = ""
	t.conflicts = conflicts
	t.mu.Unlock()

	slog.Debug("sync target complete", slog.String("remote", t.remote))
}

// ensureClone initializes the shadow clone if it doesn't exist.
func (m *Manager) ensureClone(ctx context.Context, t *syncTarget) error {
	gitDir := filepath.Join(t.cloneDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return nil // already cloned
	}

	if err := os.MkdirAll(t.cloneDir, 0o755); err != nil {
		return err
	}

	slog.Info("initializing shadow clone", slog.String("remote", t.remote), slog.String("dir", t.cloneDir))
	if err := gitCmd(ctx, t.cloneDir, "init"); err != nil {
		return err
	}
	_ = gitCmd(ctx, t.cloneDir, "checkout", "-b", "main")
	if err := gitCmd(ctx, t.cloneDir, "remote", "add", "origin", t.remote); err != nil {
		return err
	}

	// Configure committer
	_ = gitCmd(ctx, t.cloneDir, "config", "user.email", "mind-map@localhost")
	_ = gitCmd(ctx, t.cloneDir, "config", "user.name", "mind-map")

	// TODO: install pre-commit hook to scan for secrets/credentials
	// before they get pushed to a potentially public remote.

	return nil
}

// copyToWiki copies files from the shadow clone to the wiki directory.
func (m *Manager) copyToWiki(t *syncTarget) {
	for _, prefix := range t.prefixes {
		wikiDir := filepath.Join(m.wikiRoot, prefix)
		os.MkdirAll(wikiDir, 0o755)

		filepath.WalkDir(t.cloneDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(name, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(t.cloneDir, path)
			dst := filepath.Join(wikiDir, rel)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			os.WriteFile(dst, data, 0o644)
			return nil
		})
	}
}

// copyFromWiki copies files from the wiki directory to the shadow clone.
func (m *Manager) copyFromWiki(t *syncTarget) {
	for _, prefix := range t.prefixes {
		wikiDir := filepath.Join(m.wikiRoot, prefix)
		if _, err := os.Stat(wikiDir); err != nil {
			continue
		}

		filepath.WalkDir(wikiDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(name, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(wikiDir, path)
			dst := filepath.Join(t.cloneDir, rel)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			os.WriteFile(dst, data, 0o644)
			return nil
		})
	}
}

// --- helpers ---

func (t *syncTarget) setError(msg string) {
	slog.Warn("sync error", slog.String("remote", t.remote), slog.String("error", msg))
	t.mu.Lock()
	t.lastError = msg
	t.mu.Unlock()
}

func gitCmd(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func checkConflicts(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var conflicts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasSuffix(line, ".md") {
			conflicts = append(conflicts, strings.TrimSuffix(line, ".md"))
		}
	}
	return conflicts
}

func ensureGitignore(dir string) {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return
	}
	os.WriteFile(path, []byte(".mind-map.db\n.mind-map.db-wal\n.mind-map.db-shm\n"), 0o644)
}

// sanitizeDirName converts a remote URL to a safe directory name.
func sanitizeDirName(remote string) string {
	// "https://github.com/user/repo.wiki.git" -> "github.com_user_repo.wiki"
	s := remote
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}
