---
name: mind-map
description: A wiki for AI agents and humans — search, read, and write markdown pages with full-text search and wikilinks
tools:
  - search_pages
  - get_wiki_context
  - get_page
  - create_page
  - update_page
  - delete_page
  - list_pages
  - get_backlinks
---

# Mind-Map Skill

You have access to `mind-map`, an MCP server that provides a persistent wiki for storing and retrieving knowledge. Pages are plain markdown files with optional YAML frontmatter, indexed with SQLite FTS5 for full-text search. Wikilinks (`[[target]]`) create a navigable knowledge graph with backlinks.

## When to Use

Use mind-map as your **persistent memory**:
- Store decisions, architecture notes, research findings, meeting summaries
- Look up context before starting work ("what do we know about auth?")
- Build a knowledge base that grows across sessions and agents

## Getting Oriented

**Always start by understanding what's already in the wiki:**
```
get_wiki_context()
→ returns page count, top-level directories, and 20 most recently modified pages
```

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

## Best Practices

- ✅ **Search first** — check what exists before creating new pages
- ✅ **Use frontmatter** — add `title`, `type`, and `status` for structure
- ✅ **Use wikilinks** — connect related pages with `[[target]]` syntax
- ✅ **Organize by prefix** — group pages under meaningful directories
- ✅ **Get context first** — call `get_wiki_context()` to orient yourself
- ❌ **Don't create duplicates** — search before writing
- ❌ **Don't use file extensions** — paths are without `.md`

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
