package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

// testWiki creates a temporary wiki with some test pages.
func testWiki(t *testing.T) (*Wiki, string) {
	t.Helper()
	dir := t.TempDir()

	// Create test pages
	writeFile(t, dir, "index.md", `---
title: Home
---
# Welcome

This is the home page. See [[projects/mind-map]] and [[people/alice]].
`)

	writeFile(t, dir, "projects/mind-map.md", `---
title: mind-map
type: project
status: active
---
# mind-map

A wiki engine for AI agents. Built with [[Go]].

Links to [[index]] and [[people/alice]].
`)

	writeFile(t, dir, "people/alice.md", `# Alice

Alice works on [[projects/mind-map]].
`)

	writeFile(t, dir, "Go.md", `# Go

A programming language.
`)

	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	return w, dir
}

func writeFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	abs := filepath.Join(root, relPath)
	os.MkdirAll(filepath.Dir(abs), 0o755)
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func TestOpenAndPageCount(t *testing.T) {
	w, _ := testWiki(t)
	ctx, err := w.Context()
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if ctx.PageCount != 4 {
		t.Errorf("PageCount = %d, want 4", ctx.PageCount)
	}
}

func TestGetPage(t *testing.T) {
	w, _ := testWiki(t)

	p, err := w.GetPage("projects/mind-map")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if p.Title != "mind-map" {
		t.Errorf("Title = %q, want %q", p.Title, "mind-map")
	}
	if p.Frontmatter["type"] != "project" {
		t.Errorf("Frontmatter[type] = %v, want %q", p.Frontmatter["type"], "project")
	}
	// Should have links to index and people/alice and Go
	if len(p.Links) < 2 {
		t.Errorf("Links = %v, expected at least 2", p.Links)
	}
}

func TestBacklinks(t *testing.T) {
	w, _ := testWiki(t)

	backlinks, err := w.GetBacklinks("projects/mind-map")
	if err != nil {
		t.Fatalf("GetBacklinks: %v", err)
	}
	// index and people/alice both link to projects/mind-map
	if len(backlinks) != 2 {
		t.Errorf("Backlinks = %v, want 2 entries", backlinks)
	}
}

func TestSearch(t *testing.T) {
	w, _ := testWiki(t)

	results, err := w.Search("wiki engine", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("Search returned 0 results, expected at least 1")
	}
	if results[0].Path != "projects/mind-map" {
		t.Errorf("First result path = %q, want %q", results[0].Path, "projects/mind-map")
	}
}

func TestCreateAndGetPage(t *testing.T) {
	w, _ := testWiki(t)

	content := `---
title: New Page
---
# New Page

This is new. Links to [[index]].
`
	err := w.CreatePage("new-page", content)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	p, err := w.GetPage("new-page")
	if err != nil {
		t.Fatalf("GetPage after create: %v", err)
	}
	if p.Title != "New Page" {
		t.Errorf("Title = %q, want %q", p.Title, "New Page")
	}
	if len(p.Links) != 1 || p.Links[0] != "index" {
		t.Errorf("Links = %v, want [index]", p.Links)
	}
}

func TestUpdatePage(t *testing.T) {
	w, _ := testWiki(t)

	newContent := `---
title: Updated Home
---
# Updated Home

Now links to [[Go]] only.
`
	err := w.UpdatePage("index", newContent)
	if err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}

	p, err := w.GetPage("index")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if p.Title != "Updated Home" {
		t.Errorf("Title = %q, want %q", p.Title, "Updated Home")
	}
	if len(p.Links) != 1 || p.Links[0] != "Go" {
		t.Errorf("Links = %v, want [Go]", p.Links)
	}
}

func TestDeletePage(t *testing.T) {
	w, _ := testWiki(t)

	err := w.DeletePage("Go")
	if err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	_, err = w.GetPage("Go")
	if err == nil {
		t.Error("GetPage after delete should fail")
	}
}

func TestListPages(t *testing.T) {
	w, _ := testWiki(t)

	// All pages
	all, err := w.ListPages("")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("ListPages('') = %d pages, want 4", len(all))
	}

	// Filtered by prefix
	projects, err := w.ListPages("projects")
	if err != nil {
		t.Fatalf("ListPages(projects): %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("ListPages('projects') = %d pages, want 1", len(projects))
	}
}

func TestWikilinks(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"See [[foo]] and [[bar]]", []string{"foo", "bar"}},
		{"[[display|target]]", []string{"target"}},
		{"No links here", nil},
		{"[[dup]] and [[dup]]", []string{"dup"}},
		{"[[ spaces ]]", []string{"spaces"}},
	}

	for _, tt := range tests {
		got := extractWikilinks([]byte(tt.input))
		if len(got) != len(tt.want) {
			t.Errorf("extractWikilinks(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("extractWikilinks(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestContextTopLevelDirs(t *testing.T) {
	w, _ := testWiki(t)
	ctx, err := w.Context()
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	// Should have "projects" and "people"
	found := map[string]bool{}
	for _, d := range ctx.TopLevelDirs {
		found[d] = true
	}
	if !found["projects"] || !found["people"] {
		t.Errorf("TopLevelDirs = %v, expected projects and people", ctx.TopLevelDirs)
	}
}
