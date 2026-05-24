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
	"github.com/aniongithub/mind-map/internal/wiki"
)

// Reindexer is the interface the sync engine uses to trigger a wiki reindex
// after pulling changes. The returned ReindexStats are logged at INFO by
// the implementation; the sync loop itself just cares that the call
// succeeded.
type Reindexer interface {
	Reindex(ctx context.Context) (wiki.ReindexStats, error)
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
	// lfs and lfsPatterns mirror the SyncMapping fields. When lfs is
	// true the target ensures git-lfs is initialized in the shadow
	// clone and writes .gitattributes routing lfsPatterns through it.
	lfs         bool
	lfsPatterns []string

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

// RegisterMapping adds a prefix-to-remote mapping with the given
// direction, saves config, and sets up the sync target. An empty
// direction normalizes to bidirectional. Returns immediately; sync
// happens on the next cycle. LFS is left at its existing value for
// the prefix (false by default for new mappings).
func (m *Manager) RegisterMapping(prefix, remote string, direction config.SyncDirection) error {
	return m.RegisterMappingWithOptions(prefix, remote, MappingOptions{Direction: direction})
}

// MappingOptions bundles the optional knobs accepted by
// RegisterMappingWithOptions. Embedding all of them in a struct keeps
// the call site readable when more options accrete in the future.
type MappingOptions struct {
	// Direction is the sync direction. Empty value normalizes to
	// SyncBidirectional.
	Direction config.SyncDirection
	// LFS, when true, routes binary assets through git-lfs in the
	// shadow clone. Requires git-lfs on the host.
	LFS bool
	// LFSPatterns is the list of .gitattributes patterns to track
	// via LFS. Nil + LFS=true uses config.DefaultLFSPatterns. Empty
	// (non-nil) slice is "track nothing" — usable only as a stub
	// for later configuration.
	LFSPatterns []string
}

// RegisterMappingWithOptions is the full form of RegisterMapping that
// accepts the LFS knobs. The original RegisterMapping calls into this
// with no LFS to preserve back-compat with callers that only care
// about direction.
func (m *Manager) RegisterMappingWithOptions(prefix, remote string, opts MappingOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg.Sync.AddMappingWithLFS(prefix, remote, opts.Direction, opts.LFS, opts.LFSPatterns)
	if err := config.Save(m.cfgPath, m.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	m.rebuildTargetsLocked()
	slog.Info("sync mapping registered",
		slog.String("prefix", prefix),
		slog.String("remote", remote),
		slog.String("direction", string(opts.Direction.Normalize())),
		slog.Bool("lfs", opts.LFS),
	)
	return nil
}

// RegisterMappingWithLFS is a flat-argument variant of
// RegisterMappingWithOptions that satisfies the mcp package's
// SyncRegistrarWithLFS interface. Keeping the argument shape flat
// (rather than passing a struct) lets the mcp package depend only on
// stdlib types — no cross-package struct sharing.
func (m *Manager) RegisterMappingWithLFS(prefix, remote string, direction config.SyncDirection, lfs bool, lfsPatterns []string) error {
	return m.RegisterMappingWithOptions(prefix, remote, MappingOptions{
		Direction:   direction,
		LFS:         lfs,
		LFSPatterns: lfsPatterns,
	})
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
	// Build remote -> (prefixes, direction, lfs) map. Default field is
	// treated as a bidirectional mapping at the empty prefix with LFS
	// disabled (the default that works on every git provider).
	type remoteInfo struct {
		prefixes    []string
		direction   config.SyncDirection
		lfs         bool
		lfsPatterns []string
	}
	remotes := make(map[string]*remoteInfo)
	add := func(remote, prefix string, dir config.SyncDirection, lfs bool, patterns []string) {
		if remote == "" {
			return
		}
		ri, ok := remotes[remote]
		if !ok {
			ri = &remoteInfo{direction: dir, lfs: lfs, lfsPatterns: patterns}
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
		// LFS is a per-remote property in git terms (the .gitattributes
		// file lives at the root of the clone). If any mapping for a
		// remote enables LFS we honor it, but we warn if mappings
		// disagree about patterns — the operator should reconcile.
		if lfs && !ri.lfs {
			ri.lfs = true
			ri.lfsPatterns = patterns
		} else if lfs && len(patterns) > 0 && !sameStringSlice(ri.lfsPatterns, patterns) {
			slog.Warn("conflicting LFS patterns for remote, using first",
				slog.String("remote", remote),
				slog.Any("kept", ri.lfsPatterns),
				slog.Any("ignored", patterns))
		}
	}
	if m.cfg.Sync.Default != "" {
		add(m.cfg.Sync.Default, "", config.SyncBidirectional, false, nil)
	}
	for _, mapping := range m.cfg.Sync.Mappings {
		add(mapping.Remote, mapping.Prefix, mapping.Direction.Normalize(), mapping.LFS, mapping.LFSPatterns)
	}

	// Create or update targets
	for remote, ri := range remotes {
		if t, exists := m.targets[remote]; exists {
			t.prefixes = ri.prefixes
			t.direction = ri.direction
			t.lfs = ri.lfs
			t.lfsPatterns = ri.lfsPatterns
		} else {
			dirName := sanitizeDirName(remote)
			m.targets[remote] = &syncTarget{
				remote:      remote,
				cloneDir:    filepath.Join(m.syncDir, dirName),
				prefixes:    ri.prefixes,
				direction:   ri.direction,
				lfs:         ri.lfs,
				lfsPatterns: ri.lfsPatterns,
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

// sameStringSlice reports whether two []string contain the same
// elements in the same order. Used by the LFS pattern conflict check.
func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

	// LFS bootstrap (no-op when t.lfs is false). Runs on every cycle
	// so a re-registration that flipped LFS on takes effect on the
	// next tick without a manual restart. Failures here are surfaced
	// as setError + return — pushing un-tracked binaries through
	// git-lfs would just rewrite history later anyway.
	if t.lfs {
		if err := ensureLFSConfig(ctx, t); err != nil {
			t.setError(fmt.Sprintf("lfs setup: %v", err))
			return
		}
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
			if _, err := m.reindexer.Reindex(ctx); err != nil {
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
//
// Files carried: markdown pages (*.md) and the contents of *.assets/
// sidecar directories. Anything else is treated as not-our-concern
// and skipped — keeps random scratch files in the wiki tree from
// leaking to the remote, while still ferrying the asset bytes the
// image-support feature needs.
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
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(t.cloneDir, path)
			if !syncableRel(rel) {
				return nil
			}
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
//
// Carries the same file set as copyToWiki: *.md pages plus the contents
// of *.assets/ sidecar directories. Other files in the wiki tree are
// ignored.
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
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(srcRoot, path)
			if !syncableRel(rel) {
				return nil
			}
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

		// Mirror deletes: any syncable file in the clone that no longer
		// exists in the wiki must be removed so `git add -A` notices the
		// deletion. Same predicate as the copy pass to keep the two
		// directions symmetric — we'd never delete a file we wouldn't
		// have copied in the first place.
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
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(t.cloneDir, path)
			if !syncableRel(rel) {
				return nil
			}
			src := filepath.Join(srcRoot, rel)
			if _, err := os.Stat(src); os.IsNotExist(err) {
				os.Remove(path)
			}
			return nil
		})
	}
}

// syncableRel reports whether a wiki-relative path participates in
// sync. Currently:
//
//   - markdown pages (*.md)
//   - files inside per-page sidecar directories (any *.assets/ segment)
//
// New file kinds get added here as the wiki grows. Keep this in sync
// with the wiki package's storage layout — anything stored on disk
// that should travel with the wiki to a remote needs a clause here.
func syncableRel(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.HasSuffix(rel, ".md") {
		return true
	}
	// "<page>.assets/<file>" anywhere in the path. We split on slash
	// rather than checking strings.Contains so we don't accidentally
	// accept a filename that happens to embed ".assets/" as a
	// substring.
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasSuffix(seg, ".assets") {
			return true
		}
	}
	return false
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

// ensureLFSConfig initializes git-lfs in the shadow clone and writes a
// .gitattributes file routing the target's LFS patterns through LFS.
// Idempotent: re-running on a clone where LFS is already configured
// only refreshes .gitattributes if its content changed.
//
// Failure modes:
//   - git-lfs not installed → "git lfs install" fails with a clear
//     error; we surface it so the operator can install the binary
//     before retrying. The mapping itself stays registered.
//   - remote rejects LFS pointers on push (e.g. GitHub wikis) →
//     reported during the push phase, not here. We can't detect
//     this in advance without a probe push.
func ensureLFSConfig(ctx context.Context, t *syncTarget) error {
	patterns := t.lfsPatterns
	if len(patterns) == 0 {
		// Safety: if someone sets lfs=true with no patterns, fall back
		// to the default browser-image set so we at least track the
		// formats the image-support feature produces.
		patterns = config.DefaultLFSPatterns()
	}

	if err := gitCmd(ctx, t.cloneDir, "lfs", "install", "--local"); err != nil {
		return fmt.Errorf("git lfs install: %w (is git-lfs installed?)", err)
	}

	var b strings.Builder
	b.WriteString("# Managed by mind-map sync (LFS=true). Do not edit by hand;\n")
	b.WriteString("# changes will be overwritten on the next sync tick.\n")
	for _, p := range patterns {
		fmt.Fprintf(&b, "%s filter=lfs diff=lfs merge=lfs -text\n", p)
	}
	want := b.String()

	attrPath := filepath.Join(t.cloneDir, ".gitattributes")
	if existing, err := os.ReadFile(attrPath); err == nil && string(existing) == want {
		return nil
	}
	if err := os.WriteFile(attrPath, []byte(want), 0o644); err != nil {
		return fmt.Errorf("write .gitattributes: %w", err)
	}
	// Stage immediately so the next commit picks it up. The
	// "commit if there are staged changes" gate in syncTarget will
	// produce the actual commit.
	if err := gitCmd(ctx, t.cloneDir, "add", ".gitattributes"); err != nil {
		return fmt.Errorf("git add .gitattributes: %w", err)
	}
	return nil
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
