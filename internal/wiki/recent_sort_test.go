package wiki

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestListPagesRecentSortDistinguishesSameSecondMtimes is a regression
// for the "Sort: Recent" bug where pages written in the same wall-clock
// second (common after a wiki sync `git pull`) all ended up with an
// identical seconds-precision `modified` value in the index. The
// resulting ORDER BY had nothing to differentiate them, so SQLite
// returned them in an arbitrary internal order that did not match the
// actual edit recency the user expected.
//
// With nanosecond-precision storage and `path` as a stable tiebreaker,
// the most recently written file must come first.
func TestListPagesRecentSortDistinguishesSameSecondMtimes(t *testing.T) {
	tmp := t.TempDir()

	// Pre-populate three files with sub-second-spaced mtimes — all in
	// the same wall-clock second. This mirrors what `git checkout`
	// produces during a wiki sync.
	now := time.Now().UTC().Truncate(time.Second)
	files := []struct {
		path string
		mod  time.Time
	}{
		{"oldest.md", now.Add(1 * time.Millisecond)},
		{"middle.md", now.Add(2 * time.Millisecond)},
		{"newest.md", now.Add(3 * time.Millisecond)},
	}
	for _, f := range files {
		abs := filepath.Join(tmp, f.path)
		if err := os.WriteFile(abs, []byte("# "+f.path), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.path, err)
		}
		if err := os.Chtimes(abs, f.mod, f.mod); err != nil {
			t.Fatalf("chtimes %s: %v", f.path, err)
		}
	}

	w, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	pages, err := w.ListPages(context.Background(), "")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3", len(pages))
	}

	want := []string{"newest", "middle", "oldest"}
	for i, p := range pages {
		if p.Path != want[i] {
			var got []string
			for _, q := range pages {
				got = append(got, q.Path)
			}
			t.Fatalf("Recent sort wrong: got %v, want %v", got, want)
		}
	}
}
