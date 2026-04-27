package main

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"

	"github.com/aniongithub/mind-map/internal/wiki"
	mindmcp "github.com/aniongithub/mind-map/internal/mcp"
	"github.com/aniongithub/mind-map/webui"
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
	serveCmd.Flags().StringP("addr", "a", ":51849", "Address to listen on (HTTP/SSE mode)")
	serveCmd.Flags().String("webui", "", "Path to webui dist directory (overrides embedded webui)")
	serveCmd.Flags().Bool("stdio", false, "Run in stdio mode (single agent, for MCP client config)")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	useStdio, _ := cmd.Flags().GetBool("stdio")

	if useStdio {
		// stdio mode — run inline, not as a service
		w, err := wiki.Open(dir)
		if err != nil {
			return fmt.Errorf("open wiki: %w", err)
		}
		defer w.Close()

		s := mindmcp.NewServer(w)
		fmt.Fprintln(os.Stderr, "mind-map MCP server (stdio mode)")
		fmt.Fprintf(os.Stderr, "Wiki: %s\n", w.Root())
		return s.MCPServer().Run(cmd.Context(), &mcp.StdioTransport{})
	}

	// HTTP/SSE mode
	addr, _ := cmd.Flags().GetString("addr")
	webuiDir, _ := cmd.Flags().GetString("webui")

	stopCh := make(chan struct{})

	// Handle interrupt for interactive use
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		close(stopCh)
	}()

	return runHTTPServer(addr, dir, webuiDir, stopCh)
}

// runHTTPServer starts the HTTP/SSE server and blocks until stopCh is closed.
// Shared by both the interactive `serve` command and the system service.
func runHTTPServer(addr, dir, webuiDir string, stopCh chan struct{}) error {
	w, err := wiki.Open(dir)
	if err != nil {
		return fmt.Errorf("open wiki: %w", err)
	}
	defer w.Close()

	s := mindmcp.NewServer(w)

	sseHandler := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		return s.MCPServer()
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", sseHandler)

	// Serve the webui: --webui flag overrides embedded, then embedded, then fallback
	var webFS fs.FS
	if webuiDir != "" {
		if _, err := os.Stat(webuiDir); err == nil {
			webFS = os.DirFS(webuiDir)
		}
	}
	if webFS == nil {
		webFS = webui.DistFS()
	}
	if webFS != nil {
		mux.Handle("/", http.FileServerFS(webFS))
	} else {
		mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Content-Type", "text/html")
			fmt.Fprint(rw, `<!DOCTYPE html><html><body style="font-family:sans-serif;padding:40px">
				<h1>mind-map</h1><p>WebUI not built. Run <code>npm run build</code> in <code>webui/</code></p>
			</body></html>`)
		})
	}

	server := &http.Server{Addr: addr, Handler: mux}

	fmt.Fprintf(os.Stderr, "mind-map server on %s (wiki: %s)\n", addr, w.Root())
	fmt.Fprintf(os.Stderr, "MCP SSE endpoint: http://localhost%s/mcp\n", addr)

	go func() {
		<-stopCh
		server.Close()
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
