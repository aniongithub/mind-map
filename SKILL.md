---
name: mind-map
description: A wiki for AI agents and humans -- search, read, and write markdown pages with full-text search and wikilinks
tools:
  - search_pages
  - get_wiki_context
  - get_wiki_digest
  - get_page
  - create_page
  - update_page
  - delete_page
  - move_page
  - list_pages
  - get_backlinks
  - register_sync
  - reindex_wiki
---

# Mind-Map Skill

You have access to `mind-map`, an MCP server that provides a persistent wiki for storing and retrieving knowledge. Pages are plain markdown files with optional YAML frontmatter, indexed with SQLite FTS5 for full-text search. Wikilinks (`[[target]]`) create a navigable knowledge graph with backlinks.

## Connection

mind-map uses **stdio** transport. MCP clients launch it as a subprocess:

```json
{
  "mcpServers": {
    "mind-map": {
      "type": "local",
      "command": "mind-map",
      "args": [],
      "tools": ["*"]
    }
  }
}
```

The wiki directory defaults to `~/.mind-map/wiki`. Override with `--dir` if needed. A web UI is available via `mind-map serve` which starts an HTTP server.

## When to Use

Use mind-map as your **persistent memory**:
- Store decisions, architecture notes, research findings, meeting summaries
- Look up context before starting work ("what do we know about auth?")
- Build a knowledge base that grows across sessions and agents

## Getting Oriented

**Always start a new conversation with the digest:**
```
get_wiki_digest()
→ returns a compact markdown blob: page count, top word/phrase cloud
  (what this wiki is about), pages you or other agents recently
  touched (intent, not file-mtime), and per-area page counts.
  ~4 KB cap, ~1K tokens — designed to fit any context budget.
```

The digest is always-current: a background job rebuilds the cloud
every few minutes and the recents LRU updates on every page op.
Persisted to SQLite so a fresh server restart already has signal.

If you need the legacy mtime-sorted "recently modified pages" list
or the filesystem-derived top-level directory list, call:
```
get_wiki_context()
→ same shape as before, plus the digest fields layered on for free.
```

New clients should prefer `get_wiki_digest`; `get_wiki_context`
remains for backwards compatibility.

## Searching

```
search_pages(query: "authentication")
→ returns matching paths, titles, and ranked snippets
```

Use search before creating pages to avoid duplicates.

## Reading Pages

```
get_page(path: "architecture/auth")
→ returns title, body, frontmatter, outgoing links, and backlinks
```

Paths are relative, without `.md` extension (e.g. `projects/mind-map`, `decisions/use-jwt`).

## Creating Pages

```
create_page(path: "architecture/auth", content: "---\ntitle: Authentication\ntype: design-doc\nstatus: draft\n---\n# Authentication\n\nWe use JWT tokens. See [[api/tokens]].")
```

- Use YAML frontmatter for structured metadata (`title`, `type`, `status`, custom fields)
- Use `[[target]]` wikilinks to connect related pages
- Use `[[display text|target]]` for custom link text
- Organize with path prefixes (e.g. `architecture/`, `decisions/`, `meetings/`)

## Updating Pages

```
update_page(path: "architecture/auth", content: "---\ntitle: Authentication\nstatus: approved\n---\n# Authentication\n\nUpdated content here.")
```

Replaces the full page content. Read the page first to preserve existing content you want to keep.

## Deleting Pages

```
delete_page(path: "drafts/old-idea")
```

## Renaming / Moving Pages

```
move_page(from: "projects/old-name", to: "projects/new-name")
```

`move_page` is **atomic** — it renames the file on disk, updates the index, and rewrites the page's outgoing-link rows in one step. **Always use `move_page` instead of `create_page` + `delete_page`** to avoid leaving duplicate pages behind. Backlinks from other pages (other pages with `[[old-name]]` in their source) are intentionally not rewritten; if you want those updated, search and edit the source pages explicitly.

If the destination already exists, `move_page` fails with a message containing `destination already exists`. **Ask the user whether to overwrite** — the destination's content will be lost — and only then retry with `overwrite: true`:

```
move_page(from: "projects/old-name", to: "projects/new-name", overwrite: true)
```

Never set `overwrite: true` without explicit user confirmation: it is destructive.

## Listing Pages

```
list_pages()
list_pages(prefix: "architecture")
→ returns all pages, or filtered by path prefix
```

## Following the Knowledge Graph

```
get_backlinks(path: "api/tokens")
→ returns all pages that link to this page
```

Use backlinks to discover related context and navigate the wiki.

## Wiki Layout (Recommended)

mind-map doesn't enforce a directory scheme, but **agents should follow this convention** so the wiki stays organizable and so [[git sync mappings|sync prefixes]] line up with meaningful boundaries:

```
<project-or-area>/<category>/<page>
```

Concretely:

```
projects/forgewright/architecture/dag.md
projects/mind-map/concepts/wikilinks.md
notes/reading/2026-05-design-patterns.md
journal/2026-05-18.md
decisions/2026-05-18-pick-sqlite-fts5.md
```

Top-level directories should be **projects** (named after the thing you're building) or **areas** (`notes`, `journal`, `decisions`, `people`, etc.). Inside each, group by category. This:

- Keeps related pages close in the sidebar and graph view.
- Makes `prefix:`-based sync mappings useful: a single mapping like `prefix: "projects/forgewright"` can sync that subtree to its own git repo.
- Lets agents and humans navigate by intuition — "where would I put this?" usually has one obvious answer.

When in doubt, prefer **deeper paths over wider ones**. A page at `projects/foo/decisions/auth.md` is almost always better than `foo-auth-decision.md` at the root.

## Best Practices

- ✅ **Search first** — check what exists before creating new pages
- ✅ **Use frontmatter** — add `title`, `type`, and `status` for structure
- ✅ **Use wikilinks** — connect related pages with `[[target]]` syntax
- ✅ **Follow `<project>/<category>` layout** — see above
- ✅ **`move_page`, not `create_page` + `delete_page`** — atomic, preserves the link graph
- ✅ **Get context first** — call `get_wiki_context()` to orient yourself
- ❌ **Don't create duplicates** — search before writing
- ❌ **Don't use file extensions** — paths are without `.md`
- ❌ **Don't dump pages at the root** — pick a project or area

## Sync (read-only or read-write)

`register_sync` ties a path prefix to a git remote and supports three directions:

```
register_sync(prefix: "projects/foo", remote: "https://github.com/user/foo.wiki.git")
register_sync(prefix: "docs/upstream", remote: "https://example.com/upstream.wiki.git", direction: "pull")
register_sync(prefix: "publish/blog",  remote: "https://example.com/blog.wiki.git",     direction: "push")
```

- `bidirectional` (default): pull from the remote and push wiki changes back.
- `pull`: read-only mirror — remote changes flow into the wiki; the wiki never pushes. Use this when the content is owned upstream (e.g. another project's GitHub wiki).
- `push`: write-only — wiki changes flow to the remote; the remote never overwrites the wiki. Use this for publishing a curated subtree.

Re-registering the same prefix replaces the previous direction. Respect existing sync mappings: don't reshape paths under a prefix that's syncing somewhere else without confirming with the user first.

## Forcing a Reindex

```
reindex_wiki()
→ returns { total, added, updated, removed, unchanged, elapsed_ms }
```

The wiki index updates automatically on every create/update/delete/move and on every sync pull. You shouldn't normally need `reindex_wiki`. Use it only when:

- You (or the user) edited markdown files **directly on disk** outside the wiki API, and a follow-up `list_pages` or `search_pages` doesn't reflect the change.
- You suspect the index has drifted from disk (rare; usually a sign of a bug worth reporting).

The pass is incremental — unchanged files are skipped via mtime — so it's cheap to call. Prefer `update_page` / `create_page` / `delete_page` for your own edits; those keep the index synchronous without a reindex.

## Page Format Example

```markdown
---
title: Authentication Architecture
type: design-doc
status: approved
---
# Authentication Architecture

We use JWT tokens for API auth. See [[api/tokens]] for implementation.

Related: [[security/threat-model]], [[api/rate-limiting]]
```
