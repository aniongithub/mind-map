package wiki

import (
	"context"
	"testing"
)

func TestNormalizePagePath(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantErr  bool
	}{
		{"projects/foo", "projects/foo", false},
		{"/projects/foo", "projects/foo", false},
		{"./projects/foo", "projects/foo", false},
		{"projects//foo", "projects/foo", false},
		{"projects/./foo", "projects/foo", false},
		{"projects/foo/", "projects/foo", false},
		{"projects/foo.md", "projects/foo", false},
		{`projects\foo`, "projects/foo", false},
		{"foo", "foo", false},

		{"", "", true},
		{".", "", true},
		{"/", "", true},
		{"..", "", true},
		{"../foo", "", true},
		{"foo/../../bar", "", true},
	}
	for _, c := range cases {
		got, err := normalizePagePath(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizePagePath(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizePagePath(%q) returned unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizePagePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDuplicateIndexRowsViaDenormalizedPaths is a regression test for
// https://github.com/aniongithub/mind-map/issues/35: agents calling
// CreatePage / UpdatePage with equivalent denormalized path spellings
// must not produce two index rows pointing at the same on-disk file.
func TestDuplicateIndexRowsViaDenormalizedPaths(t *testing.T) {
	tmp := t.TempDir()
	w, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	ctx := context.Background()
	if err := w.CreatePage(ctx, "/projects/foo", "# Foo\nbody1"); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if err := w.UpdatePage(ctx, "projects/foo", "# Foo\nbody2"); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}

	pages, err := w.ListPages(ctx, "")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 {
		var paths []string
		for _, p := range pages {
			paths = append(paths, p.Path)
		}
		t.Fatalf("expected 1 indexed page, got %d: %v", len(pages), paths)
	}
	if pages[0].Path != "projects/foo" {
		t.Errorf("indexed path = %q, want %q", pages[0].Path, "projects/foo")
	}

	got, err := w.GetPage(ctx, "/projects/foo")
	if err != nil {
		t.Fatalf("GetPage via denormalized path: %v", err)
	}
	if got.Path != "projects/foo" {
		t.Errorf("GetPage returned path = %q, want %q", got.Path, "projects/foo")
	}

	results, err := w.Search(ctx, "Foo", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 search result, got %d", len(results))
	}
}

func TestRejectPathEscapingWikiRoot(t *testing.T) {
	tmp := t.TempDir()
	w, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if err := w.CreatePage(context.Background(), "../escape", "x"); err == nil {
		t.Error("CreatePage with traversal path should have failed")
	}
}
