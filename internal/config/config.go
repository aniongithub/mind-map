// Package config handles runtime configuration for mind-map.
// Settings are stored in a JSON file (default ~/.mind-map/config.json)
// and are separate from CLI flags which control installation-level config.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SyncDirection controls which side of the sync flow is active.
//   - "" or "bidirectional" — pull from remote and push local changes (default)
//   - "pull" — read-only: pull from remote into the wiki, never push
//   - "push" — write-only: push wiki changes upstream, ignore remote changes
type SyncDirection string

const (
	SyncBidirectional SyncDirection = "bidirectional"
	SyncPull          SyncDirection = "pull"
	SyncPush          SyncDirection = "push"
)

// Normalize returns the canonical direction value. Empty string becomes
// SyncBidirectional. Unknown values also become SyncBidirectional (safe
// default — better to over-sync than to silently no-op).
func (d SyncDirection) Normalize() SyncDirection {
	switch d {
	case SyncPull, SyncPush, SyncBidirectional:
		return d
	default:
		return SyncBidirectional
	}
}

// SyncMapping maps a wiki path prefix to a git remote.
type SyncMapping struct {
	Prefix    string        `json:"prefix"`
	Remote    string        `json:"remote"`
	Direction SyncDirection `json:"direction,omitempty"`
	// LFS, when true, configures the synced shadow clone to track
	// the patterns in LFSPatterns via git-lfs. Useful when binary
	// assets (uploaded via the image-support tools) would otherwise
	// balloon the git repo. Requires git-lfs on the host. Defaults
	// off because GitHub wikis don't support LFS — flip it on only
	// for plain repos / providers that do.
	LFS bool `json:"lfs,omitempty"`
	// LFSPatterns is the list of .gitattributes patterns to route
	// through LFS. If empty when LFS is true, a sensible default
	// (the browser-renderable image extensions plus .pdf) is used
	// — see DefaultLFSPatterns.
	LFSPatterns []string `json:"lfs_patterns,omitempty"`
}

// DefaultLFSPatterns returns the default set of file patterns to route
// through LFS when a sync mapping enables LFS but doesn't override the
// patterns explicitly. Tracks the browser-renderable image set used by
// the upload tools, plus common companion formats agents are likely to
// reach for next.
func DefaultLFSPatterns() []string {
	return []string{
		"*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp",
		"*.avif", "*.svg", "*.bmp", "*.ico",
	}
}

// SyncConfig holds git sync settings.
type SyncConfig struct {
	Enabled  bool          `json:"enabled"`
	Default  string        `json:"default"`
	Interval string        `json:"interval"`
	Mappings []SyncMapping `json:"mappings,omitempty"`
}

// ParseInterval returns the sync interval as a time.Duration.
// Returns the default (30s) if the value is empty or invalid.
func (s *SyncConfig) ParseInterval() time.Duration {
	if s.Interval == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(s.Interval)
	if err != nil || d < 5*time.Second {
		return 30 * time.Second
	}
	return d
}

// ResolveRemote returns the git remote for a given page path.
// It checks mappings (longest prefix match) then falls back to the default.
// Returns empty string if no remote matches.
func (s *SyncConfig) ResolveRemote(pagePath string) string {
	bestPrefix := ""
	bestRemote := ""
	for _, m := range s.Mappings {
		if (pagePath == m.Prefix || strings.HasPrefix(pagePath, m.Prefix+"/")) && len(m.Prefix) > len(bestPrefix) {
			bestPrefix = m.Prefix
			bestRemote = m.Remote
		}
	}
	if bestRemote != "" {
		return bestRemote
	}
	return s.Default
}

// AddMapping adds or updates a prefix-to-remote mapping with the given
// direction. An empty or unrecognized direction normalizes to
// SyncBidirectional, so callers that don't care about direction can
// pass "" (or SyncBidirectional explicitly) and get the safe default.
//
// If a mapping for prefix already exists, its remote and direction are
// both replaced — this is treated as a re-registration, not an additive
// op, so an existing mapping switching from bidirectional to pull-only
// (or vice versa) propagates cleanly. LFS settings on an existing
// mapping are preserved; use AddMappingWithLFS to update them.
func (s *SyncConfig) AddMapping(prefix, remote string, direction SyncDirection) {
	direction = direction.Normalize()
	for i, m := range s.Mappings {
		if m.Prefix == prefix {
			s.Mappings[i].Remote = remote
			s.Mappings[i].Direction = direction
			return
		}
	}
	s.Mappings = append(s.Mappings, SyncMapping{Prefix: prefix, Remote: remote, Direction: direction})
}

// AddMappingWithLFS is like AddMapping but also sets the LFS flag and
// (optionally) the LFS patterns. Patterns default to DefaultLFSPatterns
// when LFS is true and patterns is nil. Pass an empty (non-nil) slice
// to explicitly track nothing — that's a usable no-op state for an
// operator who wants to flip LFS on later.
func (s *SyncConfig) AddMappingWithLFS(prefix, remote string, direction SyncDirection, lfs bool, patterns []string) {
	direction = direction.Normalize()
	if lfs && patterns == nil {
		patterns = DefaultLFSPatterns()
	}
	for i, m := range s.Mappings {
		if m.Prefix == prefix {
			s.Mappings[i].Remote = remote
			s.Mappings[i].Direction = direction
			s.Mappings[i].LFS = lfs
			s.Mappings[i].LFSPatterns = patterns
			return
		}
	}
	s.Mappings = append(s.Mappings, SyncMapping{
		Prefix:      prefix,
		Remote:      remote,
		Direction:   direction,
		LFS:         lfs,
		LFSPatterns: patterns,
	})
}

// Remotes returns all unique remotes (default + mappings).
func (s *SyncConfig) Remotes() []string {
	seen := make(map[string]bool)
	var remotes []string
	if s.Default != "" {
		seen[s.Default] = true
		remotes = append(remotes, s.Default)
	}
	for _, m := range s.Mappings {
		if m.Remote != "" && !seen[m.Remote] {
			seen[m.Remote] = true
			remotes = append(remotes, m.Remote)
		}
	}
	return remotes
}

// Config holds all runtime settings.
type Config struct {
	Sync SyncConfig `json:"sync"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Sync: SyncConfig{
			Enabled:  false,
			Default:  "",
			Interval: "30s",
		},
	}
}

// DefaultPath returns the default config file path (~/.mind-map/config.json).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".mind-map", "config.json")
	}
	return filepath.Join(home, ".mind-map", "config.json")
}

// Load reads config from the given path. If the file doesn't exist,
// returns DefaultConfig with no error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes config to the given path, creating parent directories.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
