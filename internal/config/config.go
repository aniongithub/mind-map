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

// SyncMapping maps a wiki path prefix to a git remote.
type SyncMapping struct {
	Prefix string `json:"prefix"`
	Remote string `json:"remote"`
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

// AddMapping adds or updates a prefix-to-remote mapping.
func (s *SyncConfig) AddMapping(prefix, remote string) {
	for i, m := range s.Mappings {
		if m.Prefix == prefix {
			s.Mappings[i].Remote = remote
			return
		}
	}
	s.Mappings = append(s.Mappings, SyncMapping{Prefix: prefix, Remote: remote})
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
