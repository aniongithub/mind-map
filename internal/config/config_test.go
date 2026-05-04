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
		{"", 30 * time.Second},
		{"invalid", 30 * time.Second},
		{"2s", 30 * time.Second},
	}
	for _, tt := range tests {
		s := &SyncConfig{Interval: tt.input}
		got := s.ParseInterval()
		if got != tt.want {
			t.Errorf("ParseInterval(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestResolveRemote(t *testing.T) {
	s := &SyncConfig{
		Default: "https://github.com/user/wiki.wiki.git",
		Mappings: []SyncMapping{
			{Prefix: "projects/mind-map", Remote: "https://github.com/user/mind-map.wiki.git"},
			{Prefix: "projects/other", Remote: "https://github.com/user/other.wiki.git"},
		},
	}

	tests := []struct {
		path string
		want string
	}{
		{"projects/mind-map/design", "https://github.com/user/mind-map.wiki.git"},
		{"projects/mind-map", "https://github.com/user/mind-map.wiki.git"},
		{"projects/other/readme", "https://github.com/user/other.wiki.git"},
		{"notes/meeting", "https://github.com/user/wiki.wiki.git"},
		{"unmatched", "https://github.com/user/wiki.wiki.git"},
	}
	for _, tt := range tests {
		got := s.ResolveRemote(tt.path)
		if got != tt.want {
			t.Errorf("ResolveRemote(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}

	// No default, no match -> empty
	s2 := &SyncConfig{Mappings: []SyncMapping{
		{Prefix: "projects/x", Remote: "https://example.com/x.wiki.git"},
	}}
	if got := s2.ResolveRemote("notes/y"); got != "" {
		t.Errorf("no default: ResolveRemote(notes/y) = %q, want empty", got)
	}
}

func TestAddMapping(t *testing.T) {
	s := &SyncConfig{}

	s.AddMapping("projects/mind-map", "https://github.com/user/mind-map.wiki.git")
	if len(s.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(s.Mappings))
	}

	// Update existing
	s.AddMapping("projects/mind-map", "https://github.com/user/mind-map-v2.wiki.git")
	if len(s.Mappings) != 1 {
		t.Fatalf("expected 1 mapping after update, got %d", len(s.Mappings))
	}
	if s.Mappings[0].Remote != "https://github.com/user/mind-map-v2.wiki.git" {
		t.Errorf("mapping not updated: %q", s.Mappings[0].Remote)
	}

	// Add another
	s.AddMapping("projects/other", "https://github.com/user/other.wiki.git")
	if len(s.Mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(s.Mappings))
	}
}

func TestRemotes(t *testing.T) {
	s := &SyncConfig{
		Default: "https://github.com/user/wiki.wiki.git",
		Mappings: []SyncMapping{
			{Prefix: "a", Remote: "https://github.com/user/a.wiki.git"},
			{Prefix: "b", Remote: "https://github.com/user/wiki.wiki.git"}, // duplicate of default
		},
	}
	remotes := s.Remotes()
	if len(remotes) != 2 {
		t.Errorf("expected 2 unique remotes, got %v", remotes)
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
	cfg.Sync.Default = "https://github.com/user/wiki.wiki.git"
	cfg.Sync.Interval = "1m"
	cfg.Sync.AddMapping("projects/mind-map", "https://github.com/user/mind-map.wiki.git")

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

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
	if loaded.Sync.Default != "https://github.com/user/wiki.wiki.git" {
		t.Errorf("loaded default = %q", loaded.Sync.Default)
	}
	if len(loaded.Sync.Mappings) != 1 {
		t.Fatalf("loaded mappings count = %d, want 1", len(loaded.Sync.Mappings))
	}
	if loaded.Sync.Mappings[0].Prefix != "projects/mind-map" {
		t.Errorf("loaded mapping prefix = %q", loaded.Sync.Mappings[0].Prefix)
	}
}
