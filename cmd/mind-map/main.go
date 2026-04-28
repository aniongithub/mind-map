package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"github.com/aniongithub/mind-map/internal/logging"
	"github.com/aniongithub/mind-map/internal/wiki"
	mindmcp "github.com/aniongithub/mind-map/internal/mcp"
	"github.com/aniongithub/mind-map/webui"
	"github.com/kardianos/service"
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
	serveCmd.Flags().String("log-file", "", "Path to log file (logs to stderr and file)")
	serveCmd.Flags().Bool("stdio", false, "Run in stdio mode (single agent, for MCP client config)")
	serveCmd.Flags().Bool("run-as-service", false, "Run via kardianos/service (used by service manager)")
	serveCmd.Flags().MarkHidden("run-as-service")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	useStdio, _ := cmd.Flags().GetBool("stdio")
	logFile, _ := cmd.Flags().GetString("log-file")
	runAsService, _ := cmd.Flags().GetBool("run-as-service")

	if runAsService {
		// Launched by the OS service manager — delegate to kardianos/service
		addr, _ := cmd.Flags().GetString("addr")
		webuiDir, _ := cmd.Flags().GetString("webui")
		prg := &mindMapService{addr: addr, dir: dir, webui: webuiDir}
		svc, err := service.New(prg, newServiceConfig(addr, dir, webuiDir))
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		return svc.Run()
	}

	// Initialize logging for interactive mode (stderr + optional file)
	if f := logging.Init(nil, logFile); f != nil {
		defer f.Close()
	}

	if useStdio {
		w, err := wiki.Open(dir)
		if err != nil {
			return fmt.Errorf("open wiki: %w", err)
		}
		defer w.Close()

		s := mindmcp.NewServer(w)
		slog.Info("mind-map MCP server starting", slog.String("mode", "stdio"), slog.String("wiki", w.Root()))
		return s.MCPServer().Run(cmd.Context(), &mcp.StdioTransport{})
	}

	// HTTP/SSE mode (interactive)
	addr, _ := cmd.Flags().GetString("addr")
	webuiDir, _ := cmd.Flags().GetString("webui")

	stopCh := make(chan struct{})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		slog.Info("received interrupt, shutting down")
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

	// Wrap with panic recovery and request logging
	handler := logging.RecoverMiddleware(logging.RequestMiddleware(mux))
	server := &http.Server{Addr: addr, Handler: handler}

	slog.Info("mind-map server starting",
		slog.String("addr", addr),
		slog.String("wiki", w.Root()),
		slog.String("mcp_endpoint", "http://localhost"+addr+"/mcp"),
	)

	go func() {
		<-stopCh
		slog.Info("shutting down HTTP server")
		server.Close()
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", slog.Any("error", err))
		return err
	}
	slog.Info("server stopped")
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
