package wiki

import (
	"container/list"
	"sync"
)

// recentsLRU is a fixed-capacity, most-recently-used-first ring of page
// paths. It tracks pages the user or agent has *actively* touched —
// Create, Update, Get, Move (both ends), Delete, GetBacklinks — rather
// than what disk mtime says was changed most recently. The distinction
// matters when sync's copyToWiki bumps mtimes for files the agent never
// looked at; an mtime-based "recents" would surface those, an LRU
// reflects intent.
//
// The structure is a doubly-linked list plus a path->element index, so
// touch / remove / rename are all O(1). It is safe for concurrent use.
//
// Persistence (snapshot to SQLite on a slow ticker) lives in state.go
// and the ticker lives in the wiki lifecycle code; recentsLRU itself
// is intentionally storage-agnostic.
type recentsLRU struct {
	mu  sync.Mutex
	cap int
	// ll holds paths with the most recently used at the front.
	ll *list.List
	// idx maps path -> list element for O(1) promote/remove.
	idx map[string]*list.Element
	// dirty is true when the in-memory state has diverged from the last
	// persisted snapshot. The persistence ticker reads + clears it.
	dirty bool
	// seq is a monotonic counter bumped on every state-changing
	// operation (touch / remove / rename / load). The digest cache
	// uses it as a cheap "did anything change?" signal so it can
	// invalidate rendered output without re-comparing snapshots.
	// Wraps at uint64 max — irrelevant in practice.
	seq uint64
}

// newRecentsLRU constructs an empty LRU with the given capacity.
// A non-positive cap is clamped to the default (20).
func newRecentsLRU(cap int) *recentsLRU {
	if cap <= 0 {
		cap = 20
	}
	return &recentsLRU{
		cap: cap,
		ll:  list.New(),
		idx: make(map[string]*list.Element),
	}
}

// touch records that the given page was just used. If the page is
// already in the ring it's promoted to the front; otherwise it's
// inserted at the front and the oldest entry is evicted if the ring
// is at capacity.
//
// Empty paths are ignored — callers don't need to guard the call site.
func (r *recentsLRU) touch(path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if elem, ok := r.idx[path]; ok {
		r.ll.MoveToFront(elem)
		r.dirty = true
		r.seq++
		return
	}
	elem := r.ll.PushFront(path)
	r.idx[path] = elem
	if r.ll.Len() > r.cap {
		oldest := r.ll.Back()
		if oldest != nil {
			r.ll.Remove(oldest)
			delete(r.idx, oldest.Value.(string))
		}
	}
	r.dirty = true
	r.seq++
}

// remove drops a path from the ring. Called when a page is deleted;
// the path is gone so including it in recents would mislead the agent.
// No-op if the path isn't tracked.
func (r *recentsLRU) remove(path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	elem, ok := r.idx[path]
	if !ok {
		return
	}
	r.ll.Remove(elem)
	delete(r.idx, path)
	r.dirty = true
	r.seq++
}

// rename relabels an entry in place, preserving its position in the
// ring. Called on MovePage so the move shows up as one touch (at the
// new name) rather than two (old name drops out, new name is fresh).
//
// If `from` isn't tracked, behaves as touch(to). If `to` is already
// tracked, the older `from` entry is removed and `to` is promoted —
// this is the same as if the agent had used `to` directly.
func (r *recentsLRU) rename(from, to string) {
	if from == to {
		r.touch(to)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	fromElem, hasFrom := r.idx[from]
	toElem, hasTo := r.idx[to]

	switch {
	case hasFrom && hasTo:
		// Both present: drop `from`, promote `to`.
		r.ll.Remove(fromElem)
		delete(r.idx, from)
		r.ll.MoveToFront(toElem)
	case hasFrom:
		// Relabel in place at the same position.
		fromElem.Value = to
		delete(r.idx, from)
		r.idx[to] = fromElem
		r.ll.MoveToFront(fromElem)
	case hasTo:
		r.ll.MoveToFront(toElem)
	default:
		// Neither tracked: insert `to` fresh.
		elem := r.ll.PushFront(to)
		r.idx[to] = elem
		if r.ll.Len() > r.cap {
			oldest := r.ll.Back()
			if oldest != nil {
				r.ll.Remove(oldest)
				delete(r.idx, oldest.Value.(string))
			}
		}
	}
	r.dirty = true
	r.seq++
}

// snapshot returns the tracked paths, most recent first. The returned
// slice is owned by the caller and safe to mutate.
func (r *recentsLRU) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, r.ll.Len())
	for e := r.ll.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(string))
	}
	return out
}

// load replaces the ring's contents with the given paths (treated as
// most-recent-first). Used by the persistence layer on Wiki.Open to
// restore the last snapshot. Clears the dirty flag — what we just
// loaded matches what's on disk.
func (r *recentsLRU) load(paths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ll.Init()
	r.idx = make(map[string]*list.Element, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, dup := r.idx[p]; dup {
			continue
		}
		elem := r.ll.PushBack(p)
		r.idx[p] = elem
		if r.ll.Len() >= r.cap {
			break
		}
	}
	r.dirty = false
	r.seq++
}

// version returns the monotonic change counter. The digest cache uses
// this as an invalidation signal: cache the rendered output keyed by
// (cloudVersion, recentsVersion), and rebuild when either advances.
//
// Cheap (one lock + load) so callers can invoke it on every read.
func (r *recentsLRU) version() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// takeDirty returns whether the ring has unsaved changes and clears
// the flag in the same operation. The persistence ticker uses this to
// skip writes when nothing has changed.
func (r *recentsLRU) takeDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	was := r.dirty
	r.dirty = false
	return was
}

// peekDirty returns whether the ring has unsaved changes without
// clearing the flag. Used by the digest.Manager's tick gate so the
// "did anything change?" probe doesn't race with the write that
// follows.
func (r *recentsLRU) peekDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dirty
}

// len returns the number of tracked paths. Test helper.
func (r *recentsLRU) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ll.Len()
}
