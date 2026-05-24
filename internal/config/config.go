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
// (or vice versa) propagates cleanly.
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

// DigestConfig holds tunables for the per-conversation orientation
// digest (cloud rebuild, recents LRU, render cap, stopword extras).
// All fields are optional; zero or invalid values fall back to the
// built-in defaults. Documented in detail in mind-map/plans/digest.
type DigestConfig struct {
	// CloudSize caps the top-K terms surfaced in the word cloud.
	// Default 50. Tunable up if your wiki is large enough that 50
	// terms feels too sparse; down if context budget is tight.
	CloudSize int `json:"cloud_size,omitempty"`

	// RecentsSize caps the active-use LRU ring. Default 20. Applied
	// at wiki Open; live changes via /api/settings take effect after
	// the next server restart.
	RecentsSize int `json:"recents_size,omitempty"`

	// CloudRefresh controls how often the cloud rebuilds. Default 5m.
	// Accepts any time.ParseDuration value; values below 30 seconds
	// are clamped up so a busy wiki doesn't burn CPU.
	CloudRefresh string `json:"cloud_refresh,omitempty"`

	// StopwordsExtra extends the built-in English stopword list.
	// Words are case-folded on load. Useful for domain-specific
	// noise like "TODO" or "FIXME".
	StopwordsExtra []string `json:"stopwords_extra,omitempty"`

	// MaxRenderBytes caps the rendered markdown blob. Default 4096
	// (~1K tokens for most LLMs). Trim discipline when over: drop
	// recents, then cloud, never areas/header/footer.
	MaxRenderBytes int `json:"max_render_bytes,omitempty"`
}

// ParseCloudRefresh returns the cloud rebuild interval. Returns the
// default (5m) if empty or invalid. Floor at 30 seconds — anything
// faster is wasted CPU for a signal nobody reads that often.
func (d *DigestConfig) ParseCloudRefresh() time.Duration {
	if d.CloudRefresh == "" {
		return 5 * time.Minute
	}
	v, err := time.ParseDuration(d.CloudRefresh)
	if err != nil || v < 30*time.Second {
		return 5 * time.Minute
	}
	return v
}

// Config holds all runtime settings.
type Config struct {
	Sync   SyncConfig   `json:"sync"`
	Digest DigestConfig `json:"digest,omitempty"`
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
