package wiki

import (
	"context"
	"sort"
	"testing"
)

func TestExportPages_JustThisPage(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	// Depth 0 = just the start page
	pages, err := w.ExportPages(ctx, "index", 0)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}

	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d: %v", len(pages), pageNames(pages))
	}
	if pages[0].Path != "index" {
		t.Errorf("expected index, got %q", pages[0].Path)
	}
	if pages[0].Body == "" {
		t.Error("page body should not be empty")
	}
}

func TestExportPages_OneHop(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	// Depth 1 from index: index + its direct links (projects/mind-map, people/alice)
	pages, err := w.ExportPages(ctx, "index", 1)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}

	names := pageNames(pages)
	sort.Strings(names)
	expected := []string{"index", "people/alice", "projects/mind-map"}
	if len(names) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, names)
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("page[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestExportPages_Unlimited(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	// Depth -1 from index: follow all reachable links
	// index → projects/mind-map, people/alice
	// projects/mind-map → Go, index, people/alice
	// people/alice → projects/mind-map
	// So all 4 pages should be reachable
	pages, err := w.ExportPages(ctx, "index", -1)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}

	if len(pages) != 4 {
		t.Fatalf("expected 4 pages (all reachable), got %d: %v", len(pages), pageNames(pages))
	}
}

func TestExportPages_LeafNode(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	// Go has no outgoing links, so any depth > 0 still returns just Go
	pages, err := w.ExportPages(ctx, "Go", 5)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}

	if len(pages) != 1 {
		t.Fatalf("expected 1 page from leaf node, got %d: %v", len(pages), pageNames(pages))
	}
	if pages[0].Path != "Go" {
		t.Errorf("expected Go, got %q", pages[0].Path)
	}
}

func TestExportPages_DanglingLink(t *testing.T) {
	w, _ := testWiki(t)
	ctx := context.Background()

	// Create a page with a link to a non-existent page
	w.CreatePage(ctx, "test-dangling", "Links to [[does-not-exist]].")

	pages, err := w.ExportPages(ctx, "test-dangling", 1)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}

	// Should only contain the start page (dangling link target is skipped)
	if len(pages) != 1 {
		t.Fatalf("expected 1 page (dangling link skipped), got %d: %v", len(pages), pageNames(pages))
	}
}

func pageNames(pages []ExportPage) []string {
	names := make([]string, len(pages))
	for i, p := range pages {
		names[i] = p.Path
	}
	return names
}
