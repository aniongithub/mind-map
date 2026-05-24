package wiki

import (
	"context"
	"reflect"
	"testing"
)

func TestState_PersistAndLoadRecents(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	// Touch a few pages, then close the wiki — Close() flushes the LRU.
	if _, err := w.GetPage(ctx, "projects/mind-map"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if _, err := w.GetPage(ctx, "people/alice"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	beforeClose := w.recents.snapshot()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the same wiki directory; the LRU should rehydrate.
	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()

	got := w2.recents.snapshot()
	if !reflect.DeepEqual(got, beforeClose) {
		t.Fatalf("LRU not restored:\n  before: %v\n  after:  %v", beforeClose, got)
	}
}

func TestState_PersistAndLoadCloud(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	// Seed and persist the cloud directly (the ticker isn't running
	// in tests; Step 6 owns that wiring).
	terms := []CloudTerm{
		{Term: "wiki", Count: 5},
		{Term: "mind-map", Count: 3},
	}
	w.cloud.Set(terms)
	if err := w.persistCloud(ctx); err != nil {
		t.Fatalf("persistCloud: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()

	loaded, ok := w2.cloud.Get()
	if !ok {
		t.Fatalf("cloud not restored (ok=false)")
	}
	if !reflect.DeepEqual(loaded, terms) {
		t.Fatalf("cloud roundtrip mismatch:\n  before: %v\n  after:  %v", terms, loaded)
	}
}

func TestState_LoadFiltersStalePaths(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	// Touch a real page and a fake one. We can't get a fake into the
	// LRU via Wiki methods (they validate), so use the LRU directly.
	if _, err := w.GetPage(ctx, "index"); err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	w.recents.touch("ghost/page/that/does/not/exist")

	if err := w.persistRecents(ctx); err != nil {
		t.Fatalf("persistRecents: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen; the ghost path should be dropped on load because it
	// isn't in `pages`.
	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()

	for _, p := range w2.recents.snapshot() {
		if p == "ghost/page/that/does/not/exist" {
			t.Fatalf("stale path leaked through filter: %v", w2.recents.snapshot())
		}
	}
	// The real one survives.
	found := false
	for _, p := range w2.recents.snapshot() {
		if p == "index" {
			found = true
		}
	}
	if !found {
		t.Fatalf("real path dropped by filter: %v", w2.recents.snapshot())
	}
}

func TestState_EmptyWikiNoErrors(t *testing.T) {
	// A fresh wiki has no wiki_state rows. Open() must not error,
	// and the LRU / cloud must be empty.
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("Open empty wiki: %v", err)
	}
	defer w.Close()

	if w.recents.len() != 0 {
		t.Fatalf("expected empty LRU on fresh wiki, got %v", w.recents.snapshot())
	}
	if _, ok := w.cloud.Get(); ok {
		t.Fatalf("expected unpopulated cloud on fresh wiki")
	}
}

func TestState_CorruptRecentsRowFallsBack(t *testing.T) {
	w, dir := testWiki(t)
	ctx := context.Background()

	// Inject a malformed JSON row directly.
	if err := w.writeStateKey(ctx, stateKeyRecentLRU, "{not valid json"); err != nil {
		t.Fatalf("writeStateKey: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen must not error; LRU should be empty (load failed silently).
	// Close flushes the (just-emptied) LRU, so the corrupt row gets
	// overwritten by a valid one on shutdown — that's also fine.
	w2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen with corrupt row: %v", err)
	}
	defer w2.Close()

	if w2.recents.len() != 0 {
		t.Fatalf("expected empty LRU after corrupt row; got %v", w2.recents.snapshot())
	}
}

func TestState_PersistCloudNoOpWhenUnset(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// cloud has never been Set on this wiki; persisting must not
	// write a placeholder (would clobber a previously-good copy).
	if err := w.persistCloud(ctx); err != nil {
		t.Fatalf("persistCloud unset: %v", err)
	}
	if _, ok := w.readStateKey(ctx, stateKeyCloud); ok {
		t.Fatalf("expected no wiki_state[cloud] row when cloud is unset")
	}
}
