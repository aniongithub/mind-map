package main

import (
	"fmt"
	"os"

	"github.com/aniongithub/mind-map/internal/wiki"
	mindmcp "github.com/aniongithub/mind-map/internal/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mind-map",
	Short: "A wiki engine with MCP interface for AI agents",
	Long:  "mind-map is a wiki that stores pages as markdown files, indexes them with SQLite FTS5, and exposes everything via MCP over HTTP/SSE. AI agents and humans use the same protocol.",
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the mind-map server",
	Long:  "Starts the MCP server. Use --stdio for single-agent stdio mode, or --addr for HTTP/SSE mode (default).",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().StringP("dir", "d", ".", "Path to the wiki directory")
	serveCmd.Flags().StringP("addr", "a", ":8080", "Address to listen on (HTTP/SSE mode)")
	serveCmd.Flags().Bool("stdio", false, "Run in stdio mode (single agent, for MCP client config)")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	useStdio, _ := cmd.Flags().GetBool("stdio")

	// Open the wiki
	w, err := wiki.Open(dir)
	if err != nil {
		return fmt.Errorf("open wiki: %w", err)
	}
	defer w.Close()

	// Create MCP server
	s := mindmcp.NewServer(w)

	if useStdio {
		fmt.Fprintln(os.Stderr, "mind-map MCP server (stdio mode)")
		fmt.Fprintf(os.Stderr, "Wiki: %s\n", w.Root())
		return s.MCPServer().Run(cmd.Context(), &mcp.StdioTransport{})
	}

	// TODO: HTTP/SSE transport + static web app serving
	addr, _ := cmd.Flags().GetString("addr")
	fmt.Fprintf(os.Stderr, "mind-map server on %s (wiki: %s)\n", addr, w.Root())
	fmt.Fprintln(os.Stderr, "HTTP/SSE mode not yet implemented — use --stdio for now")
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
