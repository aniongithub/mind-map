package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		"get_page":        false,
		"create_page":     false,
		"update_page":     false,
		"delete_page":     false,
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
