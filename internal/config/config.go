// Package config handles runtime configuration for mind-map.
// Settings are stored in a JSON file (default ~/.mind-map/config.json)
// and are separate from CLI flags which control installation-level config.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SyncConfig holds git sync settings.
type SyncConfig struct {
	Enabled  bool   `json:"enabled"`
	Remote   string `json:"remote"`
	Interval string `json:"interval"`
	Token    string `json:"token,omitempty"`
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

// Config holds all runtime settings.
type Config struct {
	Sync SyncConfig `json:"sync"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Sync: SyncConfig{
			Enabled:  false,
			Remote:   "",
			Interval: "30s",
			Token:    "",
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

// Masked returns a copy of the config with sensitive fields redacted
// for API responses.
func (c *Config) Masked() *Config {
	cp := *c
	cp.Sync = c.Sync
	if cp.Sync.Token != "" {
		cp.Sync.Token = "********"
	}
	return &cp
}
