// Package mcp implements MCP tool definitions that wrap the wiki engine.
// Each tool is a thin adapter from MCP request/response to wiki operations.
package mcp

import (
	"context"
	"encoding/json"

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
	Query string `json:"query" jsonschema:"description=Search query string"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=Maximum results (default 20)"`
}

type pagePathInput struct {
	Path string `json:"path" jsonschema:"description=Page path without .md extension (e.g. projects/mind-map)"`
}

type createInput struct {
	Path    string `json:"path" jsonschema:"description=Page path without .md extension"`
	Content string `json:"content" jsonschema:"description=Markdown content (optionally with YAML frontmatter)"`
}

type updateInput struct {
	Path    string `json:"path" jsonschema:"description=Page path without .md extension"`
	Content string `json:"content" jsonschema:"description=New markdown content"`
}

type listInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"description=Filter pages by path prefix"`
}

// --- Tool handlers ---

func (s *Server) searchPages(_ context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, any, error) {
	results, err := s.wiki.Search(input.Query, input.Limit)
	if err != nil {
		return nil, nil, err
	}
	return textResult(results)
}

func (s *Server) getWikiContext(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	ctx, err := s.wiki.Context()
	if err != nil {
		return nil, nil, err
	}
	return textResult(ctx)
}

func (s *Server) getPage(_ context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	page, err := s.wiki.GetPage(input.Path)
	if err != nil {
		return nil, nil, err
	}
	return textResult(page)
}

func (s *Server) createPage(_ context.Context, _ *mcp.CallToolRequest, input createInput) (*mcp.CallToolResult, any, error) {
	if err := s.wiki.CreatePage(input.Path, input.Content); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Created page: " + input.Path},
		},
	}, nil, nil
}

func (s *Server) updatePage(_ context.Context, _ *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, any, error) {
	if err := s.wiki.UpdatePage(input.Path, input.Content); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Updated page: " + input.Path},
		},
	}, nil, nil
}

func (s *Server) deletePage(_ context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	if err := s.wiki.DeletePage(input.Path); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "Deleted page: " + input.Path},
		},
	}, nil, nil
}

func (s *Server) listPages(_ context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, any, error) {
	pages, err := s.wiki.ListPages(input.Prefix)
	if err != nil {
		return nil, nil, err
	}
	return textResult(pages)
}

func (s *Server) getBacklinks(_ context.Context, _ *mcp.CallToolRequest, input pagePathInput) (*mcp.CallToolResult, any, error) {
	backlinks, err := s.wiki.GetBacklinks(input.Path)
	if err != nil {
		return nil, nil, err
	}
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
