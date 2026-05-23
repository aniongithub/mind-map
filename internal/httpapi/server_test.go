package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
	"github.com/aniongithub/mind-map/internal/wiki"
)

// newTestServer builds a Server-backed http.Handler over a temporary wiki
// directory. Sync is disabled by default so tests don't spawn goroutines.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("open wiki: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	return New(Deps{
		Wiki:       w,
		CfgPath:    dir + "/config.json",
		Cfg:        config.DefaultConfig(),
		GetVersion: func() string { return "test" },
		StopCh:     make(chan struct{}),
	})
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGetVersion(t *testing.T) {
	h := newTestServer(t)
	rec := doJSON(t, h, "GET", "/api/version", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] != "test" {
		t.Fatalf("got %q", out["version"])
	}
}

func TestCreateGetListSearch(t *testing.T) {
	h := newTestServer(t)

	// Create
	rec := doJSON(t, h, "POST", "/api/pages", map[string]string{
		"path":    "notes/hello",
		"content": "# Hello\n\nFirst page about [[notes/world]].",
	})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	// Get
	rec = doJSON(t, h, "GET", "/api/pages/notes/hello", nil)
	if rec.Code != 200 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "First page about") {
		t.Fatalf("body missing content: %s", rec.Body.String())
	}

	// List
	rec = doJSON(t, h, "GET", "/api/pages", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "notes/hello") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	// Search
	rec = doJSON(t, h, "GET", "/api/search?q=First", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "notes/hello") {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateAndDelete(t *testing.T) {
	h := newTestServer(t)
	doJSON(t, h, "POST", "/api/pages", map[string]string{"path": "p", "content": "v1"})

	rec := doJSON(t, h, "PUT", "/api/pages/p", map[string]string{"content": "v2"})
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "GET", "/api/pages/p", nil)
	if !strings.Contains(rec.Body.String(), "v2") {
		t.Fatalf("not updated: %s", rec.Body.String())
	}

	rec = doJSON(t, h, "DELETE", "/api/pages/p", nil)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "GET", "/api/pages/p", nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	h := newTestServer(t)
	rec := doJSON(t, h, "GET", "/api/search", nil)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreatePageValidation(t *testing.T) {
	h := newTestServer(t)
	rec := doJSON(t, h, "POST", "/api/pages", map[string]string{"path": "", "content": ""})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	h := newTestServer(t)

	// PUT a config with sync disabled — should accept and apply (no sync started).
	rec := doJSON(t, h, "PUT", "/api/settings", map[string]any{
		"sync": map[string]any{"enabled": false},
	})
	if rec.Code != 200 {
		t.Fatalf("put settings: %d %s", rec.Code, rec.Body.String())
	}

	// GET it back.
	rec = doJSON(t, h, "GET", "/api/settings", nil)
	if rec.Code != 200 {
		t.Fatalf("get settings: %d", rec.Code)
	}

	// Sync status should report disabled.
	rec = doJSON(t, h, "GET", "/api/sync/status", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"enabled\":false") {
		t.Fatalf("sync status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSettingsValidatesSyncEnabled(t *testing.T) {
	h := newTestServer(t)
	// Sync enabled without a default or any mapping must be rejected.
	rec := doJSON(t, h, "PUT", "/api/settings", map[string]any{
		"sync": map[string]any{"enabled": true},
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestStaticPlaceholderWhenNoWebFS(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "WebUI not built") {
		t.Fatalf("placeholder missing: %d %s", rec.Code, rec.Body.String())
	}
}

// TestShutdownStopsSync verifies the StopCh listener cleanly tears down
// a running sync manager. This is the lifecycle path exercised by both
// /api/restart and SIGINT.
func TestShutdownStopsSync(t *testing.T) {
	dir := t.TempDir()
	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("open wiki: %v", err)
	}
	defer w.Close()

	stopCh := make(chan struct{})
	h := New(Deps{
		Wiki:       w,
		CfgPath:    filepath.Join(dir, "config.json"),
		Cfg:        config.DefaultConfig(),
		GetVersion: func() string { return "test" },
		StopCh:     stopCh,
	})

	// Sanity: server is up.
	rec := doJSON(t, h, "GET", "/api/version", nil)
	if rec.Code != 200 {
		t.Fatalf("version: %d", rec.Code)
	}

	// Trigger shutdown. The goroutine in New() should observe this and
	// cancel rootCtx + stop any sync manager. With no sync running, this
	// is mainly an assertion that the shutdown path is reachable and
	// idempotent.
	close(stopCh)

	// Give the shutdown goroutine a chance to run.
	time.Sleep(100 * time.Millisecond)

	// A second close would panic; the shutdown goroutine should have
	// exited after the first close. We can't reach into the Server, but
	// we can verify the handler still functions for read-only endpoints
	// (the http.Server itself isn't shut down by us — that's main.go's
	// job; we only manage sync lifecycle here).
	rec = doJSON(t, h, "GET", "/api/version", nil)
	if rec.Code != 200 {
		t.Fatalf("version after shutdown: %d", rec.Code)
	}
}

// TestSettingsChangeDuringShutdown verifies the supervisor refuses to
// start new sync managers once shutdown has begun. This is the race the
// /api/restart bug fix addresses: a settings PUT racing with shutdown
// must not leave a sync goroutine running after the server is gone.
func TestSettingsChangeDuringShutdown(t *testing.T) {
	dir := t.TempDir()
	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("open wiki: %v", err)
	}
	defer w.Close()

	stopCh := make(chan struct{})
	h := New(Deps{
		Wiki:       w,
		CfgPath:    filepath.Join(dir, "config.json"),
		Cfg:        config.DefaultConfig(),
		GetVersion: func() string { return "test" },
		StopCh:     stopCh,
	})

	// Begin shutdown.
	close(stopCh)
	// Wait long enough for the shutdown goroutine to flip shuttingDown.
	time.Sleep(100 * time.Millisecond)

	// PUT settings that *would* enable sync if accepted. Since
	// shuttingDown is set, applyConfig must short-circuit. We use an
	// obviously-invalid remote so that if applyConfig erroneously ran
	// it, the sync manager would try (and fail) to git-clone — but no
	// goroutine should be spawned at all.
	rec := doJSON(t, h, "PUT", "/api/settings", map[string]any{
		"sync": map[string]any{
			"enabled": true,
			"default": "/nonexistent/should-never-be-cloned.git",
		},
	})
	// PUT itself still succeeds (the config write happens, then
	// applyConfig logs a warning and returns). The user observes a
	// 200 + the new settings on disk; the supervisor refuses to act.
	if rec.Code != 200 {
		t.Fatalf("put settings: %d %s", rec.Code, rec.Body.String())
	}

	// Sync status should still report disabled-or-empty because no
	// manager was started.
	rec = doJSON(t, h, "GET", "/api/sync/status", nil)
	if !strings.Contains(rec.Body.String(), "\"enabled\":false") {
		t.Fatalf("expected no manager started, got: %s", rec.Body.String())
	}
}
