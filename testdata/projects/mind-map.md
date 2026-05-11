---
status: active
title: mind-map
type: project
---
# mind-map

A wiki engine for AI agents and humans. Built with [[Go]].

Links back to [[index]].

## Feature Status

| Feature | Status | Notes |
|---------|--------|-------|
| Wiki engine | ✅ Done | SQLite-backed, FTS5 search |
| Wikilinks | ✅ Done | [[target]] and [[target|display]] |
| Backlinks | ✅ Done | Auto-tracked in DB |
| MCP server | ✅ Done | read, write, search tools |
| Web UI | 🔧 WIP | Sidebar, markdown, mermaid |
| Git sync | 🔧 WIP | Push/pull with remotes |
| Auth | ⏳ Planned | Token-based access |

## Architecture

```mermaid
graph TD
    A[Web UI] -->|REST API| B[Go Server]
    C[MCP Client] -->|stdio/SSE| B
    B --> D[(SQLite + FTS5)]
    B --> E[Markdown Files]
    B --> F[Git Sync]
    F --> G[Remote Repos]
```
