package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Sync.Enabled {
		t.Error("default sync should be disabled")
	}
	if cfg.Sync.Interval != "30s" {
		t.Errorf("default interval = %q, want 30s", cfg.Sync.Interval)
	}
}

func TestParseInterval(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"", 30 * time.Second},          // empty -> default
		{"invalid", 30 * time.Second},   // bad value -> default
		{"2s", 30 * time.Second},        // too short -> default
	}
	for _, tt := range tests {
		s := &SyncConfig{Interval: tt.input}
		got := s.ParseInterval()
		if got != tt.want {
			t.Errorf("ParseInterval(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if cfg.Sync.Enabled {
		t.Error("missing file should return defaults")
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subdir", "config.json")

	cfg := DefaultConfig()
	cfg.Sync.Enabled = true
	cfg.Sync.Remote = "https://github.com/user/repo.wiki.git"
	cfg.Sync.Token = "ghp_secret123"
	cfg.Sync.Interval = "1m"

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Check file permissions (should be 0600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 600", perm)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Sync.Enabled {
		t.Error("loaded sync.enabled should be true")
	}
	if loaded.Sync.Remote != "https://github.com/user/repo.wiki.git" {
		t.Errorf("loaded remote = %q", loaded.Sync.Remote)
	}
	if loaded.Sync.Token != "ghp_secret123" {
		t.Errorf("loaded token = %q", loaded.Sync.Token)
	}
	if loaded.Sync.Interval != "1m" {
		t.Errorf("loaded interval = %q", loaded.Sync.Interval)
	}
}

func TestMasked(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sync.Token = "ghp_secret123"

	masked := cfg.Masked()
	if masked.Sync.Token != "********" {
		t.Errorf("masked token = %q, want ********", masked.Sync.Token)
	}
	// Original should be unchanged
	if cfg.Sync.Token != "ghp_secret123" {
		t.Error("original token was modified")
	}

	// Empty token should stay empty
	cfg2 := DefaultConfig()
	masked2 := cfg2.Masked()
	if masked2.Sync.Token != "" {
		t.Errorf("masked empty token = %q, want empty", masked2.Sync.Token)
	}
}
