// Package mcp implements MCP tool definitions that wrap the wiki engine.
// Each tool is a thin adapter from MCP request/response to wiki operations.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SyncRegistrar allows the MCP server to register sync mappings and
// check whether a page path has a sync target configured.
type SyncRegistrar interface {
	RegisterMapping(prefix, remote string, direction config.SyncDirection) error
	HasMapping(pagePath string) bool
}

// SyncRegistrarWithLFS is satisfied by sync managers that accept an
// LFS option alongside the direction. MCP's register_sync tool prefers
// this when available; if the wired registrar only implements
// SyncRegistrar (older mocks / tests), the LFS flags from the tool
// input are silently dropped and a warning is logged.
//
// We keep the LFS arguments as plain types (bool + []string) rather
// than a named struct so that *sync.Manager can implement this
// interface without the mcp package depending on the sync package's
// types or vice versa.
type SyncRegistrarWithLFS interface {
	RegisterMappingWithLFS(prefix, remote string, direction config.SyncDirection, lfs bool, lfsPatterns []string) error
}

// Server wraps a Wiki and exposes it as MCP tools.
type Server struct {
	wiki   *wiki.Wiki
	sync   SyncRegistrar
	server *mcp.Server
	// forceImagesOff, when true, makes get_page / search_pages behave
	// as if include_images and include_image_metadata are both false
	// regardless of caller request. Set by operators for token-
	// constrained deployments via SetForceImagesOff.
	forceImagesOff bool
}

// NewServer creates an MCP server backed by the given wiki.
// sync may be nil if sync is not enabled.
func NewServer(w *wiki.Wiki, sync SyncRegistrar, version string) *Server {
	if version == "" {
		version = "dev"
	}
	s := &Server{
		wiki: w,
		sync: sync,
		server: mcp.NewServer(&mcp.Implementation{
			Name:    "mind-map",
			Version: version,
		}, nil),
	}
	s.registerTools()
	return s
}

// MCPServer returns the underlying mcp.Server for transport binding.
func (s *Server) MCPServer() *mcp.Server {
	return s.server
}

func (s *Server) registerTools() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "search_pages",
		Description: "Full-text search across wiki pages by title or content. Returns matching paths, titles, and snippets.",
	}, s.searchPages)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_wiki_context",
		Description: "Get wiki orientation: page count, top-level directories, and 20 most recently modified pages. Also returns the digest (cloud_terms, recents LRU, per-area counts, rendered markdown) for new clients — older clients can ignore the extra fields.",
	}, s.getWikiContext)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_wiki_digest",
		Description: "Get a compact, always-current per-conversation orientation of this wiki. Returns: a rendered markdown blob (suitable to paste into context), a word/phrase cloud across all page bodies (what this wiki is about), an LRU of pages the user or agent has actively touched (intent, not file-mtime), and per-area page counts. Call this at the start of every new conversation. Cheaper and more deterministic than searching blindly; complements search_pages once you know what to look for.",
	}, s.getWikiDigest)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_page",
		Description: "Read a wiki page with parsed frontmatter, body, outgoing links, and backlinks. Optional flags include_images (returns referenced images as MCP image content for vision agents) and include_image_metadata (returns {path,size,mime} per image without bytes). Both default off to keep token cost predictable.",
	}, s.getPageWithFlags)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "create_page",
		Description: "Create a new wiki page. Content should be markdown, optionally with YAML frontmatter.",
	}, s.createPage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "update_page",
		Description: "Update an existing wiki page's content.",
	}, s.updatePage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "delete_page",
		Description: "Delete a wiki page.",
	}, s.deletePage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "move_page",
		Description: "Rename or relocate a wiki page atomically. Moves the underlying file from one path to another, updates the index, and rewrites the page's outgoing links. Fails if the destination already exists, unless overwrite=true. Use this instead of create_page + delete_page to avoid leaving duplicate pages behind. When the destination exists, ask the user whether to overwrite (the destination's content will be lost) before retrying with overwrite=true.",
	}, s.movePage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List wiki pages, optionally filtered by a path prefix.",
	}, s.listPages)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_backlinks",
		Description: "Get all pages that link to the specified page.",
	}, s.getBacklinks)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "register_sync",
		Description: "Register a wiki path prefix to sync with a git remote. Pages under this prefix will be synced to the given repository's wiki. The remote URL should be a git clone URL (e.g. https://github.com/user/repo.wiki.git). Direction defaults to 'bidirectional' (pull+push); use 'pull' to mirror an upstream repo read-only into the wiki, or 'push' to publish wiki content to a remote without ever pulling from it. Re-registering the same prefix replaces the previous direction. Auth uses the machine's existing git credentials.",
	}, s.registerSync)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "reindex_wiki",
		Description: "Force a full reindex pass over the wiki's on-disk markdown files. Use when you've edited files outside the wiki API and want the index (search, page list, backlinks) to reflect disk state without restarting the server. The pass is incremental — unchanged files are skipped via mtime — so it's cheap to call. Returns stats: total/added/updated/removed/unchanged/elapsed_ms.",
	}, s.reindexWiki)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "upload_image",
		Description: "Upload an image to a page's sidecar directory and return its markdown-ready path. The agent then embeds the reference (e.g. ![alt](returned/path)) via update_page or edit_page. Image bytes must be base64-encoded; supported formats track what browsers render natively (PNG, JPEG, GIF, WebP, AVIF, SVG, BMP, ICO). Collisions auto-suffix.",
	}, s.uploadImage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "download_image",
		Description: "Read an image asset and return it as MCP ImageContent so vision-capable agents can see it directly. Path is the wiki-relative asset path as it appears in markdown references.",
	}, s.downloadImage)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "delete_image",
		Description: "Remove an image asset from the wiki. Pages that still reference the deleted image will have a dangling markdown link until edited — the caller is responsible for cleaning up references. Useful for capture tooling that wants a clean canonical filename across re-runs rather than auto-suffixed duplicates.",
	}, s.deleteImage)
}

// --- Tool input types ---

type searchInput struct {
	Query string `json:"query" jsonschema:"search query string"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 20"`
}

type pagePathInput struct {
	Path string `json:"path" jsonschema:"page path without .md extension, e.g. projects/mind-map"`
}

type createInput struct {
	Path    string `json:"path" jsonschema:"page path without .md extension"`
	Content string `json:"content" jsonschema:"markdown content, optionally with YAML frontmatter"`
}

type updateInput struct {
	Path    string `json:"path" jsonschema:"page path without .md extension"`
	Content string `json:"content" jsonschema:"new markdown content"`
}

type listInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"filter pages by path prefix"`
}

type registerSyncInput struct {
	Prefix string `json:"prefix" jsonschema:"wiki path prefix to sync, e.g. projects/mind-map"`
	Remote string `json:"remote" jsonschema:"git remote URL, e.g. https://github.com/user/repo.wiki.git"`
	// Direction is optional. Omitted or empty means bidirectional.
	Direction string `json:"direction,omitempty" jsonschema:"sync direction: 'bidirectional' (default), 'pull' (mirror remote read-only into wiki), or 'push' (publish wiki to remote, never pulling)"`
	// LFS, when true, configures the synced clone to track binary
	// assets through git-lfs. Requires git-lfs on the host. Leave
	// off for GitHub wikis (which reject LFS pointers) and other
	// providers that don't support LFS.
	LFS bool `json:"lfs,omitempty" jsonschema:"if true, route binary assets through git-lfs in the synced clone. Requires git-lfs on the host. Defaults false. Do not enable for GitHub wikis (LFS unsupported)."`
	// LFSPatterns, when set, overrides the default LFS .gitattributes
	// patterns (the browser-renderable image set).
	LFSPatterns []string `json:"lfs_patterns,omitempty" jsonschema:"optional .gitattributes patterns to route through LFS. If LFS=true and this is empty, the default image-format set is used."`
}

type moveInput struct {
	From string `json:"from" jsonschema:"current page path without .md extension"`
	To   string `json:"to" jsonschema:"new page path without .md extension"`
	// Overwrite is opt-in by design. The default-false behavior matches
	// the long-standing safety contract: a move never destroys data
	// unless the caller (after asking the user) explicitly says so.
	Overwrite bool `json:"overwrite,omitempty" jsonschema:"set true to replace an existing destination page; ask the user for explicit confirmation first since the destination's content will be lost"`
}

// --- Tool handlers ---

func (s *Server) searchPages(ctx context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	results, err := s.wiki.Search(ctx, input.Query, input.Limit)
	if err != nil {
		slog.Error("tool.search_pages failed", slog.String("query", input.Query), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.search_pages", slog.String("query", input.Query), slog.Int("results", len(results)), slog.Duration("elapsed", time.Since(start)))
	return textResult(results)
}

func (s *Server) getWikiContext(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	wctx, err := s.wiki.Context(ctx)
	if err != nil {
		slog.Error("tool.get_wiki_context failed", slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.get_wiki_context", slog.Int("page_count", wctx.PageCount), slog.Duration("elapsed", time.Since(start)))
	return textResult(wctx)
}

// get_page is implemented in images.go as getPageWithFlags so the
// image-related flags live next to the rest of the image tooling.
// The old getPage handler that landed on main is intentionally
// dropped during this merge: its signature (pagePathInput, no
// flags) was superseded by getPageWithFlags (getPageInput with the
// IncludeImages / IncludeImageMetadata flags) in this branch's
// slice 3 — keeping both would mean two handlers for the same tool
// name.

func (s *Server) getWikiDigest(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	d, err := s.wiki.Digest(ctx)
	if err != nil {
		slog.Error("tool.get_wiki_digest failed", slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.get_wiki_digest",
		slog.Int("page_count", d.PageCount),
		slog.Int("cloud_terms", len(d.Cloud)),
		slog.Int("recents", len(d.Recents)),
		slog.Int("areas", len(d.Areas)),
		slog.Int("bytes", len(d.Markdown)),
		slog.Duration("elapsed", time.Since(start)),
	)
	return textResult(d)
}

func (s *Server) createPage(ctx context.Context, _ *mcp.CallToolRequest, input createInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	if err := s.wiki.CreatePage(ctx, input.Path, input.Content); err != nil {
		slog.Error("tool.create_page failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.create_page", slog.String("page", input.Path), slog.Duration("elapsed", time.Since(start)))

	content := []mcp.Content{
		&mcp.TextContent{Text: "Created page: " + input.Path},
	}

	// Check if this path has a sync mapping; if not, hint the agent
	if s.sync != nil && !s.sync.HasMapping(input.Path) {
		prefix := topPrefix(input.Path)
		if prefix != "" {
			content = append(content, &mcp.TextContent{
				Text: fmt.Sprintf("Note: '%s' has no sync mapping. If this project has a GitHub repo, "+
					"ask the user if they want to sync it, then call register_sync with the prefix and remote URL.", prefix),
			})
		}
	}

	return &mcp.CallToolResult{Content: content}, nil, nil
}

func (s *Server) updatePage(ctx context.Context, _ *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	if err := s.wiki.UpdatePage(ctx, input.Path, input.Content); err != nil {
		slog.Error("tool.update_page failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.update_page", slog.String("page", input.Path), slog.Duration("elapsed", time.Since(start)))
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Updated page: " + input.Path},
		},
	}, nil, nil
}

func (s *Server) deletePage(ctx context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	if err := s.wiki.DeletePage(ctx, input.Path); err != nil {
		slog.Error("tool.delete_page failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.delete_page", slog.String("page", input.Path), slog.Duration("elapsed", time.Since(start)))
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Deleted page: " + input.Path},
		},
	}, nil, nil
}

func (s *Server) movePage(ctx context.Context, _ *mcp.CallToolRequest, input moveInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	err := s.wiki.MovePage(ctx, input.From, input.To, wiki.MoveOptions{Overwrite: input.Overwrite})
	if err != nil {
		// Make the "destination already exists" case actionable for
		// the agent: a clear hint that overwrite=true (after user
		// confirmation) is the way forward, rather than a generic
		// failure that invites a retry loop.
		if errors.Is(err, wiki.ErrDestinationExists) {
			slog.Info("tool.move_page rejected: destination exists",
				slog.String("from", input.From), slog.String("to", input.To))
			return nil, nil, fmt.Errorf("%w. Ask the user whether to overwrite %q (its content will be lost), then retry with overwrite=true if they agree", err, input.To)
		}
		slog.Error("tool.move_page failed", slog.String("from", input.From), slog.String("to", input.To), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.move_page",
		slog.String("from", input.From),
		slog.String("to", input.To),
		slog.Bool("overwrite", input.Overwrite),
		slog.Duration("elapsed", time.Since(start)),
	)
	msg := fmt.Sprintf("Moved page: %s → %s", input.From, input.To)
	if input.Overwrite {
		msg += " (overwrote existing destination)"
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}, nil, nil
}

func (s *Server) listPages(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	pages, err := s.wiki.ListPages(ctx, input.Prefix)
	if err != nil {
		slog.Error("tool.list_pages failed", slog.String("prefix", input.Prefix), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.list_pages", slog.String("prefix", input.Prefix), slog.Int("results", len(pages)), slog.Duration("elapsed", time.Since(start)))
	return textResult(pages)
}

func (s *Server) getBacklinks(ctx context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	backlinks, err := s.wiki.GetBacklinks(ctx, input.Path)
	if err != nil {
		slog.Error("tool.get_backlinks failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.get_backlinks", slog.String("page", input.Path), slog.Int("results", len(backlinks)), slog.Duration("elapsed", time.Since(start)))
	return textResult(backlinks)
}

// textResult marshals any value to JSON and returns it as an MCP text result.
func textResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil, nil
}

func (s *Server) registerSync(_ context.Context, _ *mcp.CallToolRequest, input registerSyncInput) (*mcp.CallToolResult, any, error) {
	if s.sync == nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Sync is not enabled. Enable it in the settings page first."},
			},
		}, nil, nil
	}

	if input.Prefix == "" || input.Remote == "" {
		return nil, nil, fmt.Errorf("both prefix and remote are required")
	}

	// Validate direction up-front so a typo gives the agent a clear
	// error instead of silently being normalized to bidirectional.
	direction := config.SyncDirection(input.Direction)
	if input.Direction != "" && direction.Normalize() != direction {
		return nil, nil, fmt.Errorf("invalid direction %q: must be one of 'bidirectional', 'pull', 'push' (or omitted for bidirectional)", input.Direction)
	}
	if direction == "" {
		direction = config.SyncBidirectional
	}

	if err := s.registerSyncMapping(input.Prefix, input.Remote, direction, input.LFS, input.LFSPatterns); err != nil {
		slog.Error("tool.register_sync failed",
			slog.String("prefix", input.Prefix),
			slog.String("direction", string(direction)),
			slog.Bool("lfs", input.LFS),
			slog.Any("error", err),
		)
		return nil, nil, err
	}

	slog.Info("tool.register_sync",
		slog.String("prefix", input.Prefix),
		slog.String("remote", input.Remote),
		slog.String("direction", string(direction)),
		slog.Bool("lfs", input.LFS),
	)

	msg := fmt.Sprintf("Sync registered: pages under '%s' will sync to %s", input.Prefix, input.Remote)
	switch direction {
	case config.SyncPull:
		msg += " (pull-only: changes flow from the remote into the wiki, never back)"
	case config.SyncPush:
		msg += " (push-only: changes flow from the wiki to the remote, never back)"
	default:
		msg += " (bidirectional)"
	}
	if input.LFS {
		msg += "; binary assets routed through git-lfs"
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}, nil, nil
}

// registerSyncMapping dispatches the registration call to either the
// LFS-aware variant (when the wired registrar implements it) or the
// back-compat variant (which silently drops LFS settings). Logs a
// warning when LFS was requested but the registrar can't honor it
// so the operator isn't misled about the resulting behavior.
func (s *Server) registerSyncMapping(prefix, remote string, direction config.SyncDirection, lfs bool, patterns []string) error {
	if rw, ok := s.sync.(SyncRegistrarWithLFS); ok {
		return rw.RegisterMappingWithLFS(prefix, remote, direction, lfs, patterns)
	}
	if lfs {
		slog.Warn("register_sync LFS requested but registrar doesn't support it; falling back to non-LFS",
			slog.String("prefix", prefix),
			slog.String("remote", remote))
	}
	return s.sync.RegisterMapping(prefix, remote, direction)
}

// topPrefix extracts the top-level prefix from a page path.
// "projects/mind-map/design" -> "projects/mind-map"
// "notes" -> ""
func topPrefix(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func (s *Server) reindexWiki(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	stats, err := s.wiki.Reindex(ctx)
	if err != nil {
		slog.Error("tool.reindex_wiki failed", slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.reindex_wiki",
		slog.Int("total", stats.Total),
		slog.Int("added", stats.Added),
		slog.Int("updated", stats.Updated),
		slog.Int("removed", stats.Removed),
		slog.Int("unchanged", stats.Unchanged),
		slog.Duration("elapsed", time.Since(start)),
	)
	return textResult(stats)
}
