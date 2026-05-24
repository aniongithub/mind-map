package wiki

import (
	"context"
	"reflect"
	"testing"
)

func TestRecentsLRU_TouchAndOrder(t *testing.T) {
	r := newRecentsLRU(3)

	r.touch("a")
	r.touch("b")
	r.touch("c")
	if got, want := r.snapshot(), []string{"c", "b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after initial touches: got %v, want %v", got, want)
	}

	// Re-touching an existing entry promotes it.
	r.touch("a")
	if got, want := r.snapshot(), []string{"a", "c", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after promote: got %v, want %v", got, want)
	}
}

func TestRecentsLRU_Eviction(t *testing.T) {
	r := newRecentsLRU(2)
	r.touch("a")
	r.touch("b")
	r.touch("c") // evicts "a"

	got := r.snapshot()
	if len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Fatalf("expected [c b], got %v", got)
	}
}

func TestRecentsLRU_EmptyTouchIgnored(t *testing.T) {
	r := newRecentsLRU(5)
	r.touch("")
	if r.len() != 0 {
		t.Fatalf("empty path should not be tracked, len=%d", r.len())
	}
}

func TestRecentsLRU_Remove(t *testing.T) {
	r := newRecentsLRU(5)
	r.touch("a")
	r.touch("b")
	r.touch("c")

	r.remove("b")
	if got, want := r.snapshot(), []string{"c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after remove b: got %v, want %v", got, want)
	}

	// Remove of missing path is a no-op.
	r.remove("zzz")
	if got, want := r.snapshot(), []string{"c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("noop remove changed state: got %v, want %v", got, want)
	}
}

func TestRecentsLRU_RenameInPlace(t *testing.T) {
	r := newRecentsLRU(5)
	r.touch("a")
	r.touch("b")
	r.touch("c") // order: c b a

	// Rename "b" -> "x": should land at the front (promoted) per the
	// plan's "treat a move as active use of the new name" rule.
	r.rename("b", "x")
	if got, want := r.snapshot(), []string{"x", "c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename b->x: got %v, want %v", got, want)
	}
}

func TestRecentsLRU_RenameDestExists(t *testing.T) {
	r := newRecentsLRU(5)
	r.touch("a")
	r.touch("b")
	r.touch("c") // c b a

	// Rename "a" -> "c" (overwrite move): the old "a" entry should
	// drop out, "c" should be promoted.
	r.rename("a", "c")
	if got, want := r.snapshot(), []string{"c", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename a->c (dest exists): got %v, want %v", got, want)
	}
}

func TestRecentsLRU_RenameFromMissing(t *testing.T) {
	r := newRecentsLRU(5)
	r.touch("a")
	// Rename of an untracked source: equivalent to touching the dest.
	r.rename("zzz", "b")
	if got, want := r.snapshot(), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rename zzz->b: got %v, want %v", got, want)
	}
}

func TestRecentsLRU_LoadSnapshotRoundtrip(t *testing.T) {
	r := newRecentsLRU(5)
	r.load([]string{"a", "b", "c"})

	if got, want := r.snapshot(), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after load: got %v, want %v", got, want)
	}
	if r.takeDirty() {
		t.Fatalf("load should clear dirty flag")
	}
}

func TestRecentsLRU_LoadRespectsCapacity(t *testing.T) {
	r := newRecentsLRU(2)
	r.load([]string{"a", "b", "c", "d"})
	if r.len() != 2 {
		t.Fatalf("load should stop at cap; len=%d", r.len())
	}
}

func TestRecentsLRU_DirtyTracking(t *testing.T) {
	r := newRecentsLRU(3)
	if r.takeDirty() {
		t.Fatalf("fresh ring should not be dirty")
	}
	r.touch("a")
	if !r.takeDirty() {
		t.Fatalf("touch should mark dirty")
	}
	if r.takeDirty() {
		t.Fatalf("takeDirty should clear the flag")
	}
}

// --- integration: touches fire on the right Wiki ops ---

func TestWiki_LRUIntegration(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// testWiki() seeded the wiki, and Reindex on Open() does not touch
	// the LRU (indexing is plumbing, not "user used the page").
	if got := w.recents.snapshot(); len(got) != 0 {
		t.Fatalf("LRU should be empty after Open; got %v", got)
	}

	// GetPage touches.
	if _, err := w.GetPage(ctx, "index"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if got := w.recents.snapshot(); !reflect.DeepEqual(got, []string{"index"}) {
		t.Fatalf("after GetPage: got %v", got)
	}

	// Failed GetPage does NOT touch.
	if _, err := w.GetPage(ctx, "does/not/exist"); err == nil {
		t.Fatalf("expected error on missing page")
	}
	if got := w.recents.snapshot(); !reflect.DeepEqual(got, []string{"index"}) {
		t.Fatalf("failed GetPage polluted LRU: %v", got)
	}

	// CreatePage touches.
	if err := w.CreatePage(ctx, "scratch", "# Scratch\n"); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if got := w.recents.snapshot(); got[0] != "scratch" {
		t.Fatalf("CreatePage should put scratch at front: %v", got)
	}

	// UpdatePage touches.
	if err := w.UpdatePage(ctx, "index", "# Welcome (updated)\n"); err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	if got := w.recents.snapshot(); got[0] != "index" {
		t.Fatalf("UpdatePage should promote index: %v", got)
	}

	// GetBacklinks touches.
	if _, err := w.GetBacklinks(ctx, "projects/mind-map"); err != nil {
		t.Fatalf("GetBacklinks: %v", err)
	}
	if got := w.recents.snapshot(); got[0] != "projects/mind-map" {
		t.Fatalf("GetBacklinks should promote target: %v", got)
	}

	// MovePage renames in the LRU.
	if err := w.MovePage(ctx, "scratch", "notes/scratch", MoveOptions{}); err != nil {
		t.Fatalf("MovePage: %v", err)
	}
	snap := w.recents.snapshot()
	for _, p := range snap {
		if p == "scratch" {
			t.Fatalf("old name still in LRU after move: %v", snap)
		}
	}
	if snap[0] != "notes/scratch" {
		t.Fatalf("move dest should be at front: %v", snap)
	}

	// DeletePage removes.
	if err := w.DeletePage(ctx, "notes/scratch"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	for _, p := range w.recents.snapshot() {
		if p == "notes/scratch" {
			t.Fatalf("deleted page still in LRU: %v", w.recents.snapshot())
		}
	}
}

// CreatePage that fails (page already exists) must NOT touch.
func TestWiki_LRUNoTouchOnFailedCreate(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// Drain the LRU to a known state.
	w.recents.load(nil)

	// "index" already exists in testWiki.
	if err := w.CreatePage(ctx, "index", "# dup\n"); err == nil {
		t.Fatalf("expected CreatePage to fail on existing page")
	}
	if got := w.recents.snapshot(); len(got) != 0 {
		t.Fatalf("failed CreatePage polluted LRU: %v", got)
	}
}
