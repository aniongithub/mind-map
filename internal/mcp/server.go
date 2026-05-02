// Package mcp implements MCP tool definitions that wrap the wiki engine.
// Each tool is a thin adapter from MCP request/response to wiki operations.
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps a Wiki and exposes it as MCP tools.
type Server struct {
	wiki   *wiki.Wiki
	server *mcp.Server
}

// NewServer creates an MCP server backed by the given wiki.
func NewServer(w *wiki.Wiki) *Server {
	s := &Server{
		wiki: w,
		server: mcp.NewServer(&mcp.Implementation{
			Name:    "mind-map",
			Version: "0.1.0",
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
		Description: "Get wiki orientation: page count, top-level directories, and 20 most recently modified pages.",
	}, s.getWikiContext)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_page",
		Description: "Read a wiki page with parsed frontmatter, body, outgoing links, and backlinks.",
	}, s.getPage)

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
		Name:        "list_pages",
		Description: "List wiki pages, optionally filtered by a path prefix.",
	}, s.listPages)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "get_backlinks",
		Description: "Get all pages that link to the specified page.",
	}, s.getBacklinks)
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

func (s *Server) getPage(ctx context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	page, err := s.wiki.GetPage(ctx, input.Path)
	if err != nil {
		slog.Warn("tool.get_page failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.get_page", slog.String("page", input.Path), slog.Duration("elapsed", time.Since(start)))
	return textResult(page)
}

func (s *Server) createPage(ctx context.Context, _ *mcp.CallToolRequest, input createInput) (*mcp.CallToolResult, any, error) {
	start := time.Now()
	if err := s.wiki.CreatePage(ctx, input.Path, input.Content); err != nil {
		slog.Error("tool.create_page failed", slog.String("page", input.Path), slog.Any("error", err))
		return nil, nil, err
	}
	slog.Info("tool.create_page", slog.String("page", input.Path), slog.Duration("elapsed", time.Since(start)))
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Created page: " + input.Path},
		},
	}, nil, nil
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
