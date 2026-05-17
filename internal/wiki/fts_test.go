package wiki

import (
	"context"
	"testing"
)

// TestFTSDoesNotLeakOrphansOnUpdate is a regression for the third
// duplicate-page root cause: with the default recursive_triggers=OFF,
// `INSERT OR REPLACE INTO pages` (used by indexPage) silently leaves
// orphan docids in pages_fts. Search masks this via a JOIN today, but
// the leaked index data grows over time and any future query that
// drops the JOIN would return ghost hits.
//
// Open() now enables recursive_triggers and rebuilds pages_fts, so the
// FTS index should contain no docids for tokens that exist only in
// previous (overwritten) revisions of a page.
func TestFTSDoesNotLeakOrphansOnUpdate(t *testing.T) {
	tmp := t.TempDir()
	w, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	ctx := context.Background()

	if err := w.CreatePage(ctx, "foo", "uniqueoldtokenzxq body"); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if err := w.UpdatePage(ctx, "foo", "uniquenewtokenzxq body"); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}

	// Probe the FTS index directly — bypass the JOIN that hides leaks.
	rows, err := w.db.QueryContext(ctx,
		"SELECT rowid FROM pages_fts WHERE pages_fts MATCH 'uniqueoldtokenzxq'")
	if err != nil {
		t.Fatalf("MATCH probe: %v", err)
	}
	defer rows.Close()
	var orphans []int
	for rows.Next() {
		var rid int
		if err := rows.Scan(&rid); err == nil {
			orphans = append(orphans, rid)
		}
	}
	if len(orphans) != 0 {
		t.Errorf("pages_fts still indexes the old revision: rowids=%v", orphans)
	}

	// And the public Search must not return ghost rows for the old token.
	results, err := w.Search(ctx, "uniqueoldtokenzxq", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search returned %d hits for the old token: %+v", len(results), results)
	}
}
