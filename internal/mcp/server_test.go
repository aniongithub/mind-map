package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aniongithub/mind-map/internal/config"
	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeRegistrar captures register_sync calls so tests can assert what
// arguments the handler ultimately forwarded.
type fakeRegistrar struct {
	mu    sync.Mutex
	calls []fakeRegisterCall
}

type fakeRegisterCall struct {
	Prefix    string
	Remote    string
	Direction config.SyncDirection
}

func (f *fakeRegistrar) RegisterMapping(prefix, remote string, direction config.SyncDirection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeRegisterCall{Prefix: prefix, Remote: remote, Direction: direction})
	return nil
}

func (f *fakeRegistrar) HasMapping(_ string) bool { return false }

func (f *fakeRegistrar) lastCall(t *testing.T) fakeRegisterCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("expected RegisterMapping to be called, but it wasn't")
	}
	return f.calls[len(f.calls)-1]
}

// setupTestServerWithSync is like setupTestServer but wires a
// fakeRegistrar so register_sync tests can inspect what was forwarded.
func setupTestServerWithSync(t *testing.T) (*mcp.ClientSession, *fakeRegistrar) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, "index.md", "# Home\n")
	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("Open wiki: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	reg := &fakeRegistrar{}
	s := NewServer(w, reg, "test")

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	ct, st := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := s.MCPServer().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session, reg
}

// setupTestServer creates a wiki with test pages and connects an MCP client.
func setupTestServer(t *testing.T) *mcp.ClientSession {
	t.Helper()

	dir := t.TempDir()

	writeTestFile(t, dir, "index.md", `---
title: Home
---
# Welcome

This is the home page. See [[projects/mind-map]] and [[people/alice]].
`)
	writeTestFile(t, dir, "projects/mind-map.md", `---
title: mind-map
type: project
status: active
---
# mind-map

A wiki engine for AI agents. Built with [[Go]].
`)
	writeTestFile(t, dir, "people/alice.md", `# Alice

Alice works on [[projects/mind-map]].
`)
	writeTestFile(t, dir, "Go.md", `# Go

A programming language.
`)

	w, err := wiki.Open(dir)
	if err != nil {
		t.Fatalf("Open wiki: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	s := NewServer(w, nil, "test")

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	ct, st := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := s.MCPServer().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session
}

func writeTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	abs := filepath.Join(root, relPath)
	os.MkdirAll(filepath.Dir(abs), 0o755)
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("CallTool(%s): empty content", name)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s): expected TextContent, got %T", name, result.Content[0])
	}
	return tc.Text
}

func TestListTools(t *testing.T) {
	session := setupTestServer(t)
	ctx := context.Background()

	var tools []mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		tools = append(tools, *tool)
	}

	expected := map[string]bool{
		"search_pages":    false,
		"get_wiki_context": false,
		"get_wiki_digest": false,
		"get_page":        false,
		"create_page":     false,
		"update_page":     false,
		"delete_page":     false,
		"move_page":       false,
		"list_pages":      false,
		"get_backlinks":   false,
	}
	for _, tool := range tools {
		if _, ok := expected[tool.Name]; ok {
			expected[tool.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("tool %q not found", name)
		}
	}
}

func TestGetWikiContext(t *testing.T) {
	session := setupTestServer(t)
	text := callTool(t, session, "get_wiki_context", nil)

	var ctx wiki.WikiContext
	if err := json.Unmarshal([]byte(text), &ctx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ctx.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", ctx.PageCount)
	}
	// New digest fields should be populated on the same response so
	// existing get_wiki_context callers get the orientation upgrade
	// for free (plan open question #4 — keep old shape, add fields).
	if ctx.Markdown == "" {
		t.Errorf("expected digest markdown to be populated on get_wiki_context")
	}
	if len(ctx.Areas) == 0 {
		t.Errorf("expected areas to be populated on get_wiki_context")
	}
}

func TestGetWikiDigest(t *testing.T) {
	session := setupTestServer(t)
	text := callTool(t, session, "get_wiki_digest", nil)

	var d wiki.Digest
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, text)
	}
	if d.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", d.PageCount)
	}
	if d.Markdown == "" {
		t.Errorf("Markdown empty")
	}
	if !strings.Contains(d.Markdown, "This wiki contains") {
		t.Errorf("markdown missing header sentence:\n%s", d.Markdown)
	}
	if !strings.Contains(d.Markdown, "## Areas") {
		t.Errorf("markdown missing Areas section:\n%s", d.Markdown)
	}
	if len(d.Areas) == 0 {
		t.Errorf("expected at least one area in structured output")
	}
}

func TestGetPage(t *testing.T) {
	session := setupTestServer(t)
	text := callTool(t, session, "get_page", map[string]any{"path": "projects/mind-map"})

	var page wiki.Page
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Title != "mind-map" {
		t.Errorf("Title = %q, want %q", page.Title, "mind-map")
	}
	if page.Frontmatter["type"] != "project" {
		t.Errorf("Frontmatter[type] = %v, want %q", page.Frontmatter["type"], "project")
	}
}

func TestSearchPages(t *testing.T) {
	session := setupTestServer(t)
	text := callTool(t, session, "search_pages", map[string]any{"query": "wiki engine"})

	var results []wiki.SearchResult
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Path != "projects/mind-map" {
		t.Errorf("first result = %q, want %q", results[0].Path, "projects/mind-map")
	}
}

func TestGetBacklinks(t *testing.T) {
	session := setupTestServer(t)
	text := callTool(t, session, "get_backlinks", map[string]any{"path": "projects/mind-map"})

	var backlinks []string
	if err := json.Unmarshal([]byte(text), &backlinks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(backlinks) != 2 {
		t.Errorf("backlinks = %v, want 2 entries (index, people/alice)", backlinks)
	}
}

func TestListPages(t *testing.T) {
	session := setupTestServer(t)

	// All pages
	text := callTool(t, session, "list_pages", map[string]any{})
	var all []wiki.Page
	if err := json.Unmarshal([]byte(text), &all); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("all pages = %d, want 4", len(all))
	}

	// Filtered
	text = callTool(t, session, "list_pages", map[string]any{"prefix": "projects"})
	var filtered []wiki.Page
	if err := json.Unmarshal([]byte(text), &filtered); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("filtered pages = %d, want 1", len(filtered))
	}
}

func TestCreatePage(t *testing.T) {
	session := setupTestServer(t)

	content := "---\ntitle: New Page\n---\n# New Page\n\nLinks to [[index]].\n"
	callTool(t, session, "create_page", map[string]any{
		"path":    "new-page",
		"content": content,
	})

	// Verify via get_page
	text := callTool(t, session, "get_page", map[string]any{"path": "new-page"})
	var page wiki.Page
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Title != "New Page" {
		t.Errorf("Title = %q, want %q", page.Title, "New Page")
	}
}

func TestUpdatePage(t *testing.T) {
	session := setupTestServer(t)

	newContent := "---\ntitle: Updated Home\n---\n# Updated\n\nNow links to [[Go]] only.\n"
	callTool(t, session, "update_page", map[string]any{
		"path":    "index",
		"content": newContent,
	})

	text := callTool(t, session, "get_page", map[string]any{"path": "index"})
	var page wiki.Page
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Title != "Updated Home" {
		t.Errorf("Title = %q, want %q", page.Title, "Updated Home")
	}
}

func TestDeletePage(t *testing.T) {
	session := setupTestServer(t)

	callTool(t, session, "delete_page", map[string]any{"path": "Go"})

	// Verify it's gone — get_page should return an error result
	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_page",
		Arguments: map[string]any{"path": "Go"},
	})
	if err == nil && !result.IsError {
		t.Error("expected error after deleting page, got success")
	}
}

func TestMovePage(t *testing.T) {
	session := setupTestServer(t)

	callTool(t, session, "move_page", map[string]any{
		"from": "Go",
		"to":   "languages/Go",
	})

	text := callTool(t, session, "get_page", map[string]any{"path": "languages/Go"})
	var page wiki.Page
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Path != "languages/Go" {
		t.Errorf("Path = %q, want %q", page.Path, "languages/Go")
	}

	// Old path should now be gone.
	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_page",
		Arguments: map[string]any{"path": "Go"},
	})
	if err == nil && !result.IsError {
		t.Error("expected error fetching old path after move, got success")
	}
}

func TestMovePageDestinationExistsIsRecoverable(t *testing.T) {
	session := setupTestServer(t)
	ctx := context.Background()

	// "index" already exists in the seed data; this move should fail
	// with a message that tells the agent overwrite=true is the way
	// forward (rather than a generic error that invites a retry loop).
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "move_page",
		Arguments: map[string]any{"from": "Go", "to": "index"},
	})
	if err == nil && !result.IsError {
		t.Fatal("expected error moving onto existing destination, got success")
	}
	var text string
	if err != nil {
		text = err.Error()
	} else if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !strings.Contains(text, "destination already exists") {
		t.Errorf("error message %q does not mention 'destination already exists'", text)
	}
	if !strings.Contains(text, "overwrite=true") {
		t.Errorf("error message %q does not mention overwrite=true recovery path", text)
	}

	// Source must still be there (the failed move must not have moved anything).
	srcText := callTool(t, session, "get_page", map[string]any{"path": "Go"})
	var srcPage wiki.Page
	if err := json.Unmarshal([]byte(srcText), &srcPage); err != nil {
		t.Fatalf("unmarshal source page: %v", err)
	}
	if srcPage.Path != "Go" {
		t.Errorf("source page path = %q, want %q (failed move must not move anything)", srcPage.Path, "Go")
	}
}

func TestMovePageOverwriteSucceeds(t *testing.T) {
	session := setupTestServer(t)

	// First confirm the no-overwrite path is rejected (mirrors what the
	// agent would see before asking the user).
	ctx := context.Background()
	result, _ := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "move_page",
		Arguments: map[string]any{"from": "Go", "to": "index"},
	})
	if result == nil || !result.IsError {
		t.Fatal("expected move without overwrite to fail; agent must see this signal before retrying")
	}

	// Retry with overwrite=true (the user-confirmed path).
	text := callTool(t, session, "move_page", map[string]any{
		"from":      "Go",
		"to":        "index",
		"overwrite": true,
	})
	if !strings.Contains(text, "Moved page") || !strings.Contains(text, "overwrote existing destination") {
		t.Errorf("overwrite move response = %q; expected to mention overwrite", text)
	}

	// "index" now holds the content that used to be at "Go".
	pageText := callTool(t, session, "get_page", map[string]any{"path": "index"})
	var page wiki.Page
	if err := json.Unmarshal([]byte(pageText), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(page.Body, "A programming language") {
		t.Errorf("destination body does not contain source content: %q", page.Body)
	}

	// "Go" must no longer exist.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_page",
		Arguments: map[string]any{"path": "Go"},
	})
	if err == nil && !result.IsError {
		t.Error("expected error fetching source after overwrite move, got success")
	}
}

func TestRegisterSyncDefaultsToBidirectional(t *testing.T) {
	session, reg := setupTestServerWithSync(t)

	text := callTool(t, session, "register_sync", map[string]any{
		"prefix": "projects/example",
		"remote": "https://example.com/example.wiki.git",
	})
	if !strings.Contains(text, "bidirectional") {
		t.Errorf("response did not mention bidirectional: %q", text)
	}

	got := reg.lastCall(t)
	if got.Direction != config.SyncBidirectional {
		t.Errorf("direction forwarded to registrar = %q, want bidirectional", got.Direction)
	}
}

func TestRegisterSyncPullOnly(t *testing.T) {
	session, reg := setupTestServerWithSync(t)

	text := callTool(t, session, "register_sync", map[string]any{
		"prefix":    "docs/upstream",
		"remote":    "https://example.com/upstream.wiki.git",
		"direction": "pull",
	})
	if !strings.Contains(text, "pull-only") {
		t.Errorf("response did not mention pull-only: %q", text)
	}

	got := reg.lastCall(t)
	if got.Direction != config.SyncPull {
		t.Errorf("direction forwarded to registrar = %q, want pull", got.Direction)
	}
}

func TestRegisterSyncPushOnly(t *testing.T) {
	session, reg := setupTestServerWithSync(t)

	callTool(t, session, "register_sync", map[string]any{
		"prefix":    "publish/blog",
		"remote":    "https://example.com/blog.wiki.git",
		"direction": "push",
	})
	got := reg.lastCall(t)
	if got.Direction != config.SyncPush {
		t.Errorf("direction forwarded to registrar = %q, want push", got.Direction)
	}
}

func TestRegisterSyncRejectsInvalidDirection(t *testing.T) {
	session, reg := setupTestServerWithSync(t)
	ctx := context.Background()

	// A typo must surface as a clear error rather than being silently
	// normalized away, otherwise the agent has no signal that its
	// argument was wrong.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "register_sync",
		Arguments: map[string]any{
			"prefix":    "projects/typo",
			"remote":    "https://example.com/typo.wiki.git",
			"direction": "two-way",
		},
	})
	if err == nil && !result.IsError {
		t.Fatal("expected error for invalid direction, got success")
	}
	var text string
	if err != nil {
		text = err.Error()
	} else if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !strings.Contains(text, "invalid direction") {
		t.Errorf("error message %q does not mention 'invalid direction'", text)
	}
	if len(reg.calls) != 0 {
		t.Errorf("registrar was called despite invalid direction: %+v", reg.calls)
	}
}

func TestReindexWiki(t *testing.T) {
	session := setupTestServer(t)

	// reindex_wiki returns the stats JSON as text content.
	text := callTool(t, session, "reindex_wiki", nil)
	var stats map[string]any
	if err := json.Unmarshal([]byte(text), &stats); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	// setupTestServer seeds 4 pages. After Open()'s startup reindex
	// they're already indexed, so a fresh reindex should report
	// total=4 unchanged=4 added=0.
	if total, _ := stats["total"].(float64); int(total) != 4 {
		t.Errorf("total = %v, want 4", stats["total"])
	}
	if unchanged, _ := stats["unchanged"].(float64); int(unchanged) != 4 {
		t.Errorf("unchanged = %v, want 4", stats["unchanged"])
	}
	if _, ok := stats["elapsed_ms"]; !ok {
		t.Errorf("response missing elapsed_ms: %+v", stats)
	}
}
