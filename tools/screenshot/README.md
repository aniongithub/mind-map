# tools/screenshot

End-to-end visual test for mind-map's image-support feature. Drives a
real Chromium via Playwright against a running mind-map server,
captures screenshots of representative views, uploads them through
`POST /api/assets`, embeds the references into wiki pages, and verifies
the rendered SPA actually fetches and displays them.

This is the integration counterpart to the unit tests under
`internal/wiki`, `internal/mcp`, and `internal/httpapi`. The unit tests
prove each layer in isolation; this harness proves the whole pipeline
(upload → indexer → static handler → marked → `<img>` → static handler
again) works for real.

## Setup

Inside the devcontainer, Playwright and its browsers are installed by the
[`ghcr.io/schlich/devcontainer-features/playwright`](https://github.com/schlich/devcontainer-features/tree/main/src/playwright)
feature declared in `.devcontainer/devcontainer.json`. The browser binary and
all its Linux runtime libs land in `~/.cache/ms-playwright/` automatically
during container build — no manual `npx playwright install` step required.

The local `node_modules/` for this harness is still managed by npm because
`capture.mjs` imports `playwright` as a dependency. One `npm install` here
once after a fresh clone:

```sh
cd tools/screenshot
npm install
```

`node_modules/`, `package-lock.json`, and `captured/` are gitignored.

## Run

A mind-map server must be running first. Most local workflow:

```sh
# in one terminal
go build -o /tmp/mind-map ./cmd/mind-map
/tmp/mind-map serve --addr 127.0.0.1:4242 --dir /path/to/wiki

# in another
cd tools/screenshot
npm run capture          # take + upload + embed all CAPTURES
node verify.mjs          # verify one page renders its embed correctly
```

Override the target server with `MINDMAP_URL=http://host:port npm run
capture`. `verify.mjs` accepts `MINDMAP_TARGET=path/to/page` to point
at any page that has an embedded image.

## Caveat: sync

The harness writes via the wiki's normal `PUT /api/pages/*` path, so
in `direction: pull` configurations the next sync tick will overwrite
local edits with the upstream content. Disable sync (set
`sync.enabled: false` in `~/.mind-map/config.json`) before running the
harness if you want the edits to persist locally. Bidirectional sync
preserves local edits across ticks (commit-then-merge) but pushes them
upstream — only flip that on once you're happy with the result.

## What gets captured

`CAPTURES` in `capture.mjs` is the source of truth. Currently five
views — home/graph, page detail, search results, MCP server page,
settings modal — each embedded into a different `architecture/*` page
under a managed sentinel block (`<!-- mind-map screenshots ... -->`)
so re-runs replace the prior block instead of appending.

Screenshots also land in `./captured/` so a human can eyeball them
without firing up the SPA.
