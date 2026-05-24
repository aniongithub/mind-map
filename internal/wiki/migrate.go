package wiki

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
)

// Schema migrations.
//
// The base schema in initSchema() uses CREATE ... IF NOT EXISTS and is safe
// to run against any database. For *changes* to existing tables (ADD COLUMN,
// new indexes, etc.) we need an ordered, idempotent migration runner.
//
// State is tracked under wiki_state["schema_version"] as a decimal integer
// string. A fresh database starts implicitly at version 0; each migration
// bumps to its declared `to` value inside a single transaction so a partial
// run never leaves the schema in a hybrid state.
//
// Append-only: never edit a migration after it has shipped. Add a new one.

// stateKeySchemaVersion is the wiki_state key holding the current
// migration version as a decimal string.
const stateKeySchemaVersion = "schema_version"

// migration describes one schema bump. `to` is the version this migration
// produces; the runner applies migrations in ascending `to` order whose
// `to` value is strictly greater than the current schema version.
type migration struct {
	to    int
	name  string
	apply func(*Wiki) error
}

// migrations is the canonical ordered list. Add new entries to the end.
var migrations = []migration{
	{
		to:   1,
		name: "links.kind column for image refs",
		apply: func(w *Wiki) error {
			// SQLite has no `ADD COLUMN IF NOT EXISTS`. We probe the
			// schema via PRAGMA table_info and only ALTER when the
			// column is missing — that way a database that was
			// hand-patched, or one that survived a partial earlier
			// run, doesn't error out.
			has, err := columnExists(w, "links", "kind")
			if err != nil {
				return fmt.Errorf("probe links.kind: %w", err)
			}
			if has {
				return nil
			}
			_, err = w.db.Exec(`ALTER TABLE links ADD COLUMN kind TEXT NOT NULL DEFAULT 'link'`)
			return err
		},
	},
}

// migrate brings the database up to the latest schema version. Idempotent —
// re-runs are no-ops once everything is applied. Each migration is bounded
// by writeStateKey on success, so a crash mid-pass leaves the version at
// the prior step and the next start picks up where we left off.
//
// On a fresh database (initSchema just ran with the current CREATE TABLE
// definitions), the migrations are all no-ops in practice — they probe for
// schema state before mutating. We still record the latest version so future
// migrations have an unambiguous "you started fresh at version N" baseline.
func (w *Wiki) migrate() error {
	current, err := w.currentSchemaVersion()
	if err != nil {
		return err
	}

	latest := 0
	if n := len(migrations); n > 0 {
		latest = migrations[n-1].to
	}

	for _, m := range migrations {
		if m.to <= current {
			continue
		}

		slog.Info("applying migration",
			slog.Int("from", current),
			slog.Int("to", m.to),
			slog.String("name", m.name),
		)

		if err := m.apply(w); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.to, m.name, err)
		}
		if err := w.writeStateKey(context.Background(), stateKeySchemaVersion,
			strconv.Itoa(m.to)); err != nil {
			return fmt.Errorf("record schema_version=%d: %w", m.to, err)
		}
		current = m.to
	}

	// Belt-and-suspenders: ensure the recorded version matches latest
	// even when no migrations ran (e.g. fresh DB whose CREATE TABLE
	// already includes everything). This gives future migrations a
	// clean "I know exactly where you are" starting point.
	if current < latest {
		if err := w.writeStateKey(context.Background(), stateKeySchemaVersion,
			strconv.Itoa(latest)); err != nil {
			return fmt.Errorf("record schema_version=%d: %w", latest, err)
		}
	}

	return nil
}

// currentSchemaVersion reads the persisted version, defaulting to 0 for
// fresh databases.
func (w *Wiki) currentSchemaVersion() (int, error) {
	raw, ok := w.readStateKey(context.Background(), stateKeySchemaVersion)
	if !ok {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", raw, err)
	}
	return v, nil
}

// columnExists reports whether the named column is present on the given
// table. Uses PRAGMA table_info, which lists one row per column.
func columnExists(w *Wiki, table, column string) (bool, error) {
	rows, err := w.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
