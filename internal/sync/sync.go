// Package sync provides git-based wiki synchronization.
// It pulls from and pushes to remote git repositories on an interval,
// keeping wiki pages synchronized across machines. Each prefix-to-remote
// mapping gets its own shadow clone, so multiple repos can sync independently.
package sync

import (
	"bytes"
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
	remote    string
	cloneDir  string
	prefixes  []string // wiki prefixes that map to this remote
	direction config.SyncDirection

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

// Reload swaps in a new configuration and rebuilds sync targets without
// interrupting the background loop. Callers that need to handle an
// enabled/disabled transition or an interval change should Stop and create
// a fresh Manager instead — those changes can't be applied in-place.
func (m *Manager) Reload(newCfg *config.Config) error {
	if newCfg == nil {
		return fmt.Errorf("nil config")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = newCfg
	m.rebuildTargetsLocked()
	slog.Info("sync manager reloaded", slog.Int("targets", len(m.targets)))
	return nil
}

// Interval returns the configured sync interval. Used by supervisors to
// detect interval changes that require a full restart.
func (m *Manager) Interval() time.Duration {
	return m.interval
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
	// Build remote -> (prefixes, direction) map. Default field is treated
	// as a bidirectional mapping at the empty prefix.
	type remoteInfo struct {
		prefixes  []string
		direction config.SyncDirection
	}
	remotes := make(map[string]*remoteInfo)
	add := func(remote, prefix string, dir config.SyncDirection) {
		if remote == "" {
			return
		}
		ri, ok := remotes[remote]
		if !ok {
			ri = &remoteInfo{direction: dir}
			remotes[remote] = ri
		}
		ri.prefixes = append(ri.prefixes, prefix)
		// First mapping wins for direction. If a later mapping for the
		// same remote disagrees, log a warning — there's no sane way to
		// run a single shadow clone with two opposing directions.
		if ri.direction != dir {
			slog.Warn("conflicting sync directions for remote, using first",
				slog.String("remote", remote),
				slog.String("kept", string(ri.direction)),
				slog.String("ignored", string(dir)))
		}
	}
	if m.cfg.Sync.Default != "" {
		add(m.cfg.Sync.Default, "", config.SyncBidirectional)
	}
	for _, mapping := range m.cfg.Sync.Mappings {
		add(mapping.Remote, mapping.Prefix, mapping.Direction.Normalize())
	}

	// Create or update targets
	for remote, ri := range remotes {
		if t, exists := m.targets[remote]; exists {
			t.prefixes = ri.prefixes
			t.direction = ri.direction
		} else {
			dirName := sanitizeDirName(remote)
			m.targets[remote] = &syncTarget{
				remote:    remote,
				cloneDir:  filepath.Join(m.syncDir, dirName),
				prefixes:  ri.prefixes,
				direction: ri.direction,
			}
		}
	}

	// Remove targets no longer in config
	for remote := range m.targets {
		if _, exists := remotes[remote]; !exists {
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

// syncTarget syncs a single remote.
//
// The flow is "commit-then-merge", which is the only ordering that
// preserves local-only changes against a concurrent remote:
//
//  1. (if wantPush) stage local wiki state into the clone and commit it,
//     so local edits are part of HEAD before we merge anything in.
//  2. Fetch origin and merge. Git's 3-way merge resolves overlapping
//     changes; if it conflicts, conflict markers stay in the clone and
//     surface to the user via copyToWiki + the Status conflicts list.
//  3. (if wantPull) mirror the merged clone state back to the wiki dir
//     and reindex. Files whose content is unchanged are skipped to
//     avoid bumping mtime and triggering pointless reindex churn.
//  4. (if wantPush) push HEAD to origin.
//
// Doing it in the other order (merge before staging local) lets a
// freshly-pulled remote file overwrite a local edit that landed between
// sync ticks — see TestLocalUpdateSurvivesBidirectionalSync.
//
// Direction modes:
//   - bidirectional (default): all four phases
//   - pull: skip phases 1 and 4; never copies wiki → clone
//   - push: skip phase 3; never copies clone → wiki, never reindexes
func (m *Manager) syncTarget(ctx context.Context, t *syncTarget) {
	t.mu.Lock()
	t.lastError = ""
	t.mu.Unlock()

	direction := t.direction
	if direction == "" {
		direction = config.SyncBidirectional
	}
	wantPull := direction == config.SyncBidirectional || direction == config.SyncPull
	wantPush := direction == config.SyncBidirectional || direction == config.SyncPush

	// Ensure clone exists. Even push-only needs a working clone.
	if err := m.ensureClone(ctx, t); err != nil {
		t.setError(fmt.Sprintf("clone: %v", err))
		return
	}

	// Phase 1: stage local wiki state in the clone and commit it before
	// pulling. This is what prevents local writes from being clobbered by
	// the merge in phase 2.
	if wantPush {
		m.copyFromWiki(t)
		ensureGitignore(t.cloneDir)
		if err := gitCmd(ctx, t.cloneDir, "add", "-A"); err != nil {
			t.setError(fmt.Sprintf("add: %v", err))
			return
		}
		// Only commit if there are staged changes.
		if err := gitCmd(ctx, t.cloneDir, "diff", "--cached", "--quiet"); err != nil {
			hostname, _ := os.Hostname()
			msg := fmt.Sprintf("sync from %s at %s", hostname, time.Now().UTC().Format(time.RFC3339))
			if err := gitCmd(ctx, t.cloneDir, "commit", "-m", msg); err != nil {
				t.setError(fmt.Sprintf("commit: %v", err))
				return
			}
		}
	}

	// Phase 2: fetch + merge. The merge is what reconciles local commits
	// (from phase 1) with new remote work. Pull-only also needs the merge
	// to advance HEAD; push-only needs it as a fast-forward base so the
	// later push isn't rejected.
	if err := gitCmd(ctx, t.cloneDir, "fetch", "origin"); err != nil {
		t.setError(fmt.Sprintf("fetch: %v", err))
		return
	}
	if err := gitCmd(ctx, t.cloneDir, "rev-parse", "--verify", "origin/main"); err == nil {
		if err := gitCmd(ctx, t.cloneDir, "merge", "origin/main", "--allow-unrelated-histories", "--no-edit"); err != nil {
			slog.Warn("merge conflict", slog.String("remote", t.remote), slog.Any("error", err))
		}
	} else if err := gitCmd(ctx, t.cloneDir, "rev-parse", "--verify", "origin/master"); err == nil {
		// GitHub wikis default to the 'master' branch — try it as a
		// fallback when 'main' doesn't exist.
		if err := gitCmd(ctx, t.cloneDir, "merge", "origin/master", "--allow-unrelated-histories", "--no-edit"); err != nil {
			slog.Warn("merge conflict", slog.String("remote", t.remote), slog.Any("error", err))
		}
	}

	// Conflicts (if any) are computed against the now-merged tree.
	conflicts := checkConflicts(ctx, t.cloneDir)

	// Phase 3: mirror merged clone state back to wiki and reindex.
	if wantPull {
		m.copyToWiki(t)
		if m.reindexer != nil {
			if err := m.reindexer.Reindex(ctx); err != nil {
				slog.Warn("reindex after pull failed", slog.Any("error", err))
			}
		}
	}

	// Phase 4: push.
	if wantPush {
		// Only push if we have any commits at all (a fresh clone with no
		// initial pull and no local content will have none).
		if err := gitCmd(ctx, t.cloneDir, "rev-parse", "HEAD"); err == nil {
			if err := gitCmd(ctx, t.cloneDir, "push", "-u", "origin", "main"); err != nil {
				t.setError(fmt.Sprintf("push: %v", err))
				return
			}
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
// Only files whose content differs from the destination are written, so
// unchanged pages don't get their mtime bumped on every sync — that
// matters because the wiki indexer uses mtime to decide what to re-parse.
//
// The clone is always rooted at the wiki-side prefix level: a target
// with prefix "projects/alpha" maps the root of the shadow clone into
// wikiRoot/projects/alpha. An empty prefix mirrors the whole clone
// to the wiki root. This matches copyFromWiki (the reverse direction).
func (m *Manager) copyToWiki(t *syncTarget) {
	for _, prefix := range t.prefixes {
		dstRoot := filepath.Join(m.wikiRoot, prefix)
		os.MkdirAll(dstRoot, 0o755)

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
			dst := filepath.Join(dstRoot, rel)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
				return nil
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, data, 0o644)
			return nil
		})
	}
}

// copyFromWiki copies files from the wiki directory to the shadow clone.
// Mirrors copyToWiki's prefix semantics: wikiRoot/prefix → cloneDir.
// Skips writes for identical files so git doesn't observe spurious
// "modified" entries on otherwise-clean trees, and removes clone-side
// files that no longer exist in the wiki so deletions propagate.
func (m *Manager) copyFromWiki(t *syncTarget) {
	for _, prefix := range t.prefixes {
		srcRoot := filepath.Join(m.wikiRoot, prefix)
		if _, err := os.Stat(srcRoot); err != nil {
			continue
		}

		filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
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
			rel, _ := filepath.Rel(srcRoot, path)
			dst := filepath.Join(t.cloneDir, rel)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
				return nil
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, data, 0o644)
			return nil
		})

		// Mirror deletes: any .md in the clone that no longer exists in
		// the wiki must be removed so `git add -A` notices the deletion.
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
			src := filepath.Join(srcRoot, rel)
			if _, err := os.Stat(src); os.IsNotExist(err) {
				os.Remove(path)
			}
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
