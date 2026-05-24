package wiki

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigest_StructuralFields(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	d, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	if d.PageCount == 0 {
		t.Fatal("page count should be > 0")
	}
	if d.Markdown == "" {
		t.Fatal("markdown should not be empty")
	}
	// testWiki creates pages under projects/ and people/ — at least
	// two areas should surface.
	if len(d.Areas) < 2 {
		t.Fatalf("expected >= 2 areas, got %d: %v", len(d.Areas), d.Areas)
	}
	// Cloud is empty because the ticker hasn't run yet (cold start).
	// That's the expected behavior; the digest should still render.
	if d.Cloud != nil && len(d.Cloud) != 0 {
		t.Fatalf("cloud should be empty on cold start, got %v", d.Cloud)
	}
}

func TestDigest_MarkdownShape(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// Seed cloud so we exercise the "About:" line too.
	w.cloud.Set([]CloudTerm{
		{Term: "wiki", Count: 10},
		{Term: "mind-map", Count: 7},
	})
	// Seed recents.
	w.recents.touch("projects/mind-map")
	w.recents.touch("index")
	// Bust the digest cache because we mutated state directly.
	w.digest.invalidate()

	d, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	md := d.Markdown
	t.Logf("rendered:\n%s", md)

	mustContain := []string{
		"This wiki contains",
		"About:",
		"wiki, mind-map",
		"## Areas",
		"## Recently active",
		"- index",
		"- projects/mind-map",
		"Full skill: SKILL.md",
		"get_wiki_digest",
	}
	for _, s := range mustContain {
		if !strings.Contains(md, s) {
			t.Errorf("markdown missing %q\n---\n%s\n---", s, md)
		}
	}
}

func TestDigest_AreaCountsAndIndexTitle(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// Add an index page under "projects" with a known title.
	if err := w.CreatePage(ctx, "projects/index", `---
title: Active Projects
---
# Active Projects
`); err != nil {
		t.Fatalf("create projects/index: %v", err)
	}

	d, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	var found *AreaSummary
	for i := range d.Areas {
		if d.Areas[i].Path == "projects" {
			found = &d.Areas[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("projects area missing: %+v", d.Areas)
	}
	if found.IndexTitle != "Active Projects" {
		t.Errorf("expected index title 'Active Projects', got %q", found.IndexTitle)
	}
	if found.PageCount < 2 {
		t.Errorf("projects should have >=2 pages (mind-map + index), got %d", found.PageCount)
	}

	// The rendered area line should include the index title quoted.
	if !strings.Contains(d.Markdown, `projects/index: "Active Projects"`) {
		t.Errorf("markdown missing index title:\n%s", d.Markdown)
	}
}

func TestDigest_CacheHit(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// First call populates the cache.
	first, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("first Digest: %v", err)
	}

	// Second call with no state change returns the *same* pointer
	// (the cache stores the *Digest; a hit returns it as-is).
	second, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("second Digest: %v", err)
	}
	if first != second {
		t.Errorf("expected cache hit to return same *Digest pointer")
	}
}

func TestDigest_CacheInvalidatedByLRUChange(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	first, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	// Touching the LRU bumps recents seq → cache miss next read.
	w.recents.touch("index")

	second, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Errorf("expected fresh *Digest after recents change")
	}
	if !strings.Contains(second.Markdown, "- index") {
		t.Errorf("new recents not reflected in markdown:\n%s", second.Markdown)
	}
}

func TestDigest_CacheInvalidatedByCloudChange(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	first, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	w.cloud.Set([]CloudTerm{{Term: "wiki", Count: 1}})

	second, err := w.Digest(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Errorf("expected fresh *Digest after cloud set")
	}
	if !strings.Contains(second.Markdown, "About:") {
		t.Errorf("cloud not reflected in markdown:\n%s", second.Markdown)
	}
}

func TestRenderDigest_TrimToMaxBytes(t *testing.T) {
	// Build a digest that's deliberately over-cap.
	cloud := make([]CloudTerm, 50)
	for i := range cloud {
		cloud[i] = CloudTerm{Term: strings.Repeat("x", 20), Count: 1}
	}
	recents := make([]string, 50)
	for i := range recents {
		recents[i] = strings.Repeat("path", 20)
	}
	d := &Digest{
		PageCount: 100,
		Cloud:     cloud,
		Recents:   recents,
		Areas:     []AreaSummary{{Path: "a", PageCount: 5}},
	}

	const cap = 512
	md := renderDigestMarkdown(d, cap)
	if len(md) > cap {
		// The trimmer is best-effort: if the unavoidable parts
		// (areas + header + footer) already exceed cap we accept
		// being over. But in this test those are tiny, so we
		// should be under.
		t.Errorf("rendered len=%d > cap=%d", len(md), cap)
	}
	// Areas + header + footer must still be intact.
	mustContain := []string{"## Areas", "- a (5)", "Full skill"}
	for _, s := range mustContain {
		if !strings.Contains(md, s) {
			t.Errorf("trim dropped required section %q:\n%s", s, md)
		}
	}
}

func TestRenderDigest_NoCloudNoRecents(t *testing.T) {
	d := &Digest{
		PageCount: 3,
		Areas: []AreaSummary{
			{Path: "notes", PageCount: 3},
		},
	}
	md := renderDigestMarkdown(d, 0)
	if strings.Contains(md, "About:") {
		t.Errorf("empty cloud should not produce About: line:\n%s", md)
	}
	if strings.Contains(md, "## Recently active") {
		t.Errorf("empty recents should not produce section:\n%s", md)
	}
	if !strings.Contains(md, "## Areas") || !strings.Contains(md, "- notes (3)") {
		t.Errorf("areas missing:\n%s", md)
	}
}

func TestAreaSummaries_FlatRootedPagesIgnored(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// `index` is flat-rooted; should not produce an "index" area.
	areas, err := w.areaSummaries(ctx)
	if err != nil {
		t.Fatalf("areaSummaries: %v", err)
	}
	for _, a := range areas {
		if a.Path == "index" {
			t.Fatalf("flat-rooted page leaked into areas: %+v", areas)
		}
	}
}

func TestReindex_RemovesFromLRU(t *testing.T) {
	w, dir := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// Touch and verify presence.
	if _, err := w.GetPage(ctx, "index"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	found := false
	for _, p := range w.recents.snapshot() {
		if p == "index" {
			found = true
		}
	}
	if !found {
		t.Fatalf("index should be in LRU after GetPage")
	}

	// Raw-filesystem delete + reindex (simulating sync removing a file).
	if err := os.Remove(filepath.Join(dir, "index.md")); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if _, err := w.Reindex(ctx); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	for _, p := range w.recents.snapshot() {
		if p == "index" {
			t.Fatalf("reindex should have purged stale LRU entry: %v", w.recents.snapshot())
		}
	}
}
