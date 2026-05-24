package wiki

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// wiki_state schema: a small key/value table for cross-restart persistence
// of derived structures (recents LRU, word/phrase cloud). Distinct from
// the `pages` index, which is rebuildable from disk — wiki_state holds
// signals that *can't* be recovered from the markdown files alone:
//
//   - "recent_lru" — the active-use ring (intent, not mtime). Lost on
//     restart without persistence; that's exactly the case the digest
//     plan is designed to avoid.
//   - "cloud" — the word/phrase cloud is rebuildable but expensive
//     (one full table scan + tokenization). Persisting it means a
//     freshly-restarted server has a digest immediately, not after
//     the first ticker tick (up to 5 minutes later).
//
// We intentionally do NOT persist the rendered digest markdown: it's
// sub-ms to re-assemble from cloud + LRU, and the in-memory
// digestCache already covers "don't re-format on every hit".

const (
	stateKeyRecentLRU = "recent_lru"
	stateKeyCloud     = "cloud"
)

// initStateSchema creates the wiki_state table. Called from initSchema.
// Idempotent.
func (w *Wiki) initStateSchema() error {
	_, err := w.db.Exec(`
	CREATE TABLE IF NOT EXISTS wiki_state (
		key     TEXT PRIMARY KEY,
		value   TEXT NOT NULL,
		updated TEXT NOT NULL
	);`)
	return err
}

// recentsState is the on-disk shape of the persisted LRU. Stored as a
// JSON document under wiki_state["recent_lru"].value. Items are listed
// most-recent-first, matching recentsLRU.snapshot().
type recentsState struct {
	Items []string `json:"items"`
}

// cloudState is the on-disk shape of the persisted cloud.
type cloudState struct {
	Terms []CloudTerm `json:"terms"`
}

// loadState pulls the persisted LRU + cloud out of wiki_state into
// memory. Called once at the end of Open(), after Reindex. Failures
// are logged but non-fatal — a missing or corrupt row just means the
// process starts with an empty signal, which is the same state a
// brand-new wiki ships with.
func (w *Wiki) loadState(ctx context.Context) {
	if items, ok := w.readStateKey(ctx, stateKeyRecentLRU); ok {
		var s recentsState
		if err := json.Unmarshal([]byte(items), &s); err != nil {
			slog.Warn("wiki_state recent_lru parse failed", slog.Any("error", err))
		} else {
			// Filter against the current index so paths that vanished
			// while the server was off (deleted, renamed via raw
			// filesystem, or sync-pulled away) don't reappear in the
			// LRU as 404 candidates. Reindex has already run by this
			// point, so `pages` is the authoritative set.
			filtered := w.filterAgainstIndex(ctx, s.Items)
			w.recents.load(filtered)
			slog.Info("recents loaded from wiki_state",
				slog.Int("persisted", len(s.Items)),
				slog.Int("kept", len(filtered)),
			)
		}
	}

	if terms, ok := w.readStateKey(ctx, stateKeyCloud); ok {
		var s cloudState
		if err := json.Unmarshal([]byte(terms), &s); err != nil {
			slog.Warn("wiki_state cloud parse failed", slog.Any("error", err))
		} else {
			// Use the persisted cloud as-is. The cloud is global
			// frequency counts, not per-page references — even if
			// some pages have vanished the previous distribution
			// is still a reasonable approximation until the next
			// rebuild ticker fires (default: within 5 minutes of
			// startup).
			w.cloud.Set(s.Terms)
			slog.Info("cloud loaded from wiki_state", slog.Int("terms", len(s.Terms)))
		}
	}
}

// filterAgainstIndex returns only those paths that currently exist in
// the `pages` table, preserving input order. Used on Open() to drop
// stale persisted recents whose underlying pages vanished while the
// server was off.
//
// One query: SELECT path FROM pages where path IN (...). We do it via
// a map probe rather than a SQL IN-clause because (a) the input slice
// is small (~20 entries by default) and (b) building a variable-length
// IN-clause with placeholders for SQLite is awkward.
func (w *Wiki) filterAgainstIndex(ctx context.Context, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	rows, err := w.db.QueryContext(ctx, "SELECT path FROM pages")
	if err != nil {
		slog.Warn("filterAgainstIndex query failed", slog.Any("error", err))
		return paths // fail open: keep all, let the next CRUD reconcile
	}
	defer rows.Close()
	present := make(map[string]struct{})
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			present[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := present[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// readStateKey returns the value for a wiki_state key, or "", false if
// not present or the read failed. Read errors other than "no row" are
// logged so a real DB problem doesn't silently degrade the digest.
func (w *Wiki) readStateKey(ctx context.Context, key string) (string, bool) {
	var value string
	err := w.db.QueryRowContext(ctx, "SELECT value FROM wiki_state WHERE key = ?", key).Scan(&value)
	if err == nil {
		return value, true
	}
	// sql.ErrNoRows is the common case (first run on a wiki) — silent.
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	slog.Warn("wiki_state read failed", slog.String("key", key), slog.Any("error", err))
	return "", false
}

// writeStateKey upserts a wiki_state row. The (key, value, updated)
// triple is atomic via INSERT OR REPLACE — readers either see the old
// or the new value, never a torn write.
func (w *Wiki) writeStateKey(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := w.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO wiki_state (key, value, updated) VALUES (?, ?, ?)",
		key, value, now,
	)
	return err
}

// persistRecents writes the current LRU snapshot to wiki_state. Called
// by the 30s persistence ticker (Step 6) and from Close() for a clean
// shutdown. Safe to call concurrently with reads — the LRU snapshot is
// taken under its own lock and the SQLite write is atomic.
//
// If the LRU's dirty flag is unset, this is still safe to call (we'll
// rewrite the same bytes); callers wanting to skip a redundant write
// should gate on takeDirty() before calling.
func (w *Wiki) persistRecents(ctx context.Context) error {
	state := recentsState{Items: w.recents.snapshot()}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal recents: %w", err)
	}
	return w.writeStateKey(ctx, stateKeyRecentLRU, string(data))
}

// persistCloud writes the current cloud cache to wiki_state. Called
// after a successful rebuild (Step 6). No-ops if the cloud has never
// been populated — there's nothing meaningful to write yet, and we
// don't want to clobber a previously-good persisted copy with an
// empty placeholder.
func (w *Wiki) persistCloud(ctx context.Context) error {
	terms, ok := w.cloud.Get()
	if !ok {
		return nil
	}
	state := cloudState{Terms: terms}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal cloud: %w", err)
	}
	return w.writeStateKey(ctx, stateKeyCloud, string(data))
}

// PersistRecents is the exported entry point for the digest.Manager's
// 30-second flush ticker. The internal persistRecents helper is also
// called by Close() for a clean shutdown flush.
//
// PersistRecents clears the LRU's dirty flag on success: a follow-up
// RecentsDirty() will report false until the next touch. Callers that
// want to skip a redundant write should peek with RecentsDirty before
// calling this; PersistRecents itself always writes.
func (w *Wiki) PersistRecents(ctx context.Context) error {
	if err := w.persistRecents(ctx); err != nil {
		return err
	}
	// Clear dirty only after a successful write — if the write failed,
	// the in-memory state is still ahead of disk and the next tick
	// should retry.
	w.recents.takeDirty()
	return nil
}

// RecentsDirty reports whether the LRU has unsaved changes since the
// last successful PersistRecents. Read-only — does not clear the flag.
// The digest.Manager uses this to skip redundant writes on an idle
// server.
func (w *Wiki) RecentsDirty() bool {
	return w.recents.peekDirty()
}
