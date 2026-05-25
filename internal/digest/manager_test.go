package digest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aniongithub/mind-map/internal/wiki"
)

// testWiki creates a temporary wiki with a few seed pages so the
// cloud rebuild has something to count. Kept private to this test
// file — the public wiki package has its own testWiki, but we can't
// import test helpers across packages.
func testWiki(t *testing.T) *wiki.Wiki {
	t.Helper()
	dir := t.TempDir()

	pages := map[string]string{
		"index.md":             "# Home\n\nThis wiki is about mind-map, digest, and SQLite.\n",
		"projects/mind-map.md": "# mind-map\n\nA wiki engine. SQLite-backed. Digest support.\n",
		"notes/sqlite.md":      "# SQLite\n\nSQLite is fast and embedded. mind-map uses SQLite.\n",
	}
	for name, content := range pages {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestManager_StartTriggersImmediateCloudRebuild(t *testing.T) {
	w := testWiki(t)

	m := NewManager(w, Options{
		// Long tick so the ticker doesn't fire during the test —
		// we want to assert the *synchronous* initial build only.
		CloudRefresh:   time.Hour,
		RecentsRefresh: time.Hour,
	})
	m.Start(context.Background())
	defer m.Stop()

	// After Start, the cloud cache should be populated and the digest
	// markdown should contain an About: line.
	d, err := w.Digest(context.Background())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if len(d.Cloud) == 0 {
		t.Fatalf("cloud should be populated after Start, got empty")
	}
	if !strings.Contains(d.Markdown, "About:") {
		t.Fatalf("digest missing About: line:\n%s", d.Markdown)
	}
}

func TestManager_StopIsIdempotent(t *testing.T) {
	w := testWiki(t)
	m := NewManager(w, Options{CloudRefresh: time.Hour, RecentsRefresh: time.Hour})
	m.Start(context.Background())

	m.Stop()
	m.Stop() // second Stop must not panic or block
}

func TestManager_StopWithoutStartIsNoOp(t *testing.T) {
	w := testWiki(t)
	m := NewManager(w, Options{})
	m.Stop() // must not panic, must not hang
}

func TestManager_RecentsFlushOnTick(t *testing.T) {
	w := testWiki(t)
	ctx := context.Background()

	m := NewManager(w, Options{
		CloudRefresh:   time.Hour,
		RecentsRefresh: 50 * time.Millisecond,
	})
	m.Start(ctx)
	defer m.Stop()

	// Touch via a real Wiki op so dirty flips on.
	if _, err := w.GetPage(ctx, "index"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if !w.RecentsDirty() {
		t.Fatalf("LRU should be dirty after GetPage")
	}

	// Wait for the ticker to fire and flush.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !w.RecentsDirty() {
			return // success: ticker flushed and cleared dirty
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("LRU still dirty after 1s; ticker did not flush")
}

func TestManager_StopFlushesRecents(t *testing.T) {
	w := testWiki(t)
	ctx := context.Background()

	m := NewManager(w, Options{
		// Long ticks so only the Stop-time flush can save us.
		CloudRefresh:   time.Hour,
		RecentsRefresh: time.Hour,
	})
	m.Start(ctx)

	if _, err := w.GetPage(ctx, "index"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if !w.RecentsDirty() {
		t.Fatalf("LRU should be dirty after touch")
	}

	m.Stop()

	if w.RecentsDirty() {
		t.Fatalf("Stop should have flushed dirty LRU; still dirty")
	}
}
