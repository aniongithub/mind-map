package wiki

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestImageRefsIndexed verifies that markdown image references are recorded
// in the links table with kind='image' and don't leak into the wikilink
// query surface (GetBacklinks, AllLinks, GetPage.Links).
func TestImageRefsIndexed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "guide.md", `# Guide

Here's a diagram:

![architecture](guide.assets/architecture.png)

And a wikilink to [[index]]. Also an external image
![external](https://example.com/foo.png) that should NOT be indexed.
`)
	writeFile(t, dir, "index.md", `# Index

See [[guide]].
`)

	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	ctx := context.Background()

	// 1. Image row must exist in `links` with kind='image'.
	rows, err := w.db.QueryContext(ctx, "SELECT source, target, kind FROM links WHERE kind = 'image'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	type rec struct {
		source, target, kind string
	}
	var imgs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.source, &r.target, &r.kind); err != nil {
			t.Fatal(err)
		}
		imgs = append(imgs, r)
	}
	rows.Close()
	if len(imgs) != 1 {
		t.Fatalf("got %d image rows, want 1: %+v", len(imgs), imgs)
	}
	if got := imgs[0]; got.source != "guide" || got.target != "guide.assets/architecture.png" || got.kind != "image" {
		t.Errorf("image row = %+v, want {guide, guide.assets/architecture.png, image}", got)
	}

	// 2. Wikilink query path must not include the image target.
	links, err := w.getLinks(ctx, "guide")
	if err != nil {
		t.Fatalf("getLinks: %v", err)
	}
	if len(links) != 1 || links[0] != "index" {
		t.Errorf("getLinks(guide) = %v, want [index]", links)
	}

	// 3. Backlinks for the image asset (via the kind='image' query)
	// should surface the embedding page.
	imgBacklinks := queryBacklinks(t, w, "guide.assets/architecture.png", "image")
	if len(imgBacklinks) != 1 || imgBacklinks[0] != "guide" {
		t.Errorf("image backlinks = %v, want [guide]", imgBacklinks)
	}

	// 4. Backlinks for the page must not include the image target as a source.
	pageBacklinks, err := w.GetBacklinks(ctx, "guide")
	if err != nil {
		t.Fatalf("GetBacklinks: %v", err)
	}
	if len(pageBacklinks) != 1 || pageBacklinks[0] != "index" {
		t.Errorf("GetBacklinks(guide) = %v, want [index]", pageBacklinks)
	}

	// 5. AllLinks excludes image edges.
	all, err := w.AllLinks(ctx)
	if err != nil {
		t.Fatalf("AllLinks: %v", err)
	}
	for _, l := range all {
		if l.Target == "guide.assets/architecture.png" {
			t.Errorf("AllLinks leaked image edge: %+v", l)
		}
	}
}

// TestImageRefDeduplication ensures the same image referenced multiple
// times from one page produces a single row, not duplicates.
func TestImageRefDeduplication(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "page.md", `# Page

![one](page.assets/a.png)
![two](page.assets/a.png)
![three](page.assets/a.png)
`)
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	var n int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM links WHERE source='page' AND kind='image'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("image row count = %d, want 1", n)
	}
}

// TestImageRefReindexCleansStale verifies that removing an image reference
// from a page and re-indexing drops the link row.
func TestImageRefReindexCleansStale(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "page.md")
	if err := os.WriteFile(abs, []byte(`# Page

![v1](page.assets/v1.png)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if err := w.UpdatePage(context.Background(), "page", `# Page

![v2](page.assets/v2.png)
`); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}

	var n int
	if err := w.db.QueryRow(
		"SELECT COUNT(*) FROM links WHERE source='page' AND kind='image' AND target=?",
		"page.assets/v1.png").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("stale v1 image ref still present (%d rows)", n)
	}

	if err := w.db.QueryRow(
		"SELECT COUNT(*) FROM links WHERE source='page' AND kind='image' AND target=?",
		"page.assets/v2.png").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("v2 image ref not indexed (%d rows)", n)
	}
}

// TestMigrationIdempotent runs Open twice on the same directory to confirm
// the migration runner doesn't trip on its second pass.
func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "p.md", "# P\n")

	w1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	v1 := schemaVersion(t, w1)
	w1.Close()

	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	t.Cleanup(func() { w2.Close() })
	v2 := schemaVersion(t, w2)

	if v1 != v2 {
		t.Errorf("schema_version changed across reopens: %d -> %d", v1, v2)
	}
	if v1 == 0 {
		t.Errorf("schema_version still 0 after migrate; expected latest > 0")
	}
}

// TestMigrationFromLegacySchema simulates a database that pre-dates the
// kind column by manually creating the old links schema, then verifies
// that Open transparently upgrades it.
func TestMigrationFromLegacySchema(t *testing.T) {
	dir := t.TempDir()

	// Build a "legacy" database by opening, then dropping kind, then
	// resetting schema_version. This is the simplest portable way to
	// fake a pre-migration state without checking in a binary fixture.
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open seed: %v", err)
	}
	// SQLite can't DROP COLUMN before 3.35; emulate by rebuilding.
	if _, err := w.db.Exec(`
		DROP TABLE links;
		CREATE TABLE links (
			source TEXT NOT NULL,
			target TEXT NOT NULL,
			PRIMARY KEY (source, target)
		);
		CREATE INDEX idx_links_target ON links(target);
		DELETE FROM wiki_state WHERE key = 'schema_version';
	`); err != nil {
		t.Fatalf("legacy reset: %v", err)
	}
	// Pre-seed a wikilink row in the old shape.
	if _, err := w.db.Exec(`INSERT INTO links (source, target) VALUES ('a', 'b')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	w.Close()

	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open post-legacy: %v", err)
	}
	t.Cleanup(func() { w2.Close() })

	// kind column must exist now.
	has, err := columnExists(w2, "links", "kind")
	if err != nil {
		t.Fatalf("columnExists: %v", err)
	}
	if !has {
		t.Fatal("kind column not added by migration")
	}

	// The legacy row must have been backfilled with kind='link'.
	var kind string
	if err := w2.db.QueryRow(`SELECT kind FROM links WHERE source='a' AND target='b'`).Scan(&kind); err != nil {
		t.Fatalf("query legacy row: %v", err)
	}
	if kind != "link" {
		t.Errorf("legacy row backfill kind = %q, want %q", kind, "link")
	}
}

// --- helpers ---

func queryBacklinks(t *testing.T, w *Wiki, target, kind string) []string {
	t.Helper()
	rows, err := w.db.Query("SELECT source FROM links WHERE target = ? AND kind = ?", target, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func schemaVersion(t *testing.T, w *Wiki) int {
	t.Helper()
	raw, ok := w.readStateKey(context.Background(), stateKeySchemaVersion)
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parse schema_version %q: %v", raw, err)
	}
	return v
}
