package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
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
	serveCmd.Flags().Duration("idle-timeout", 60*time.Second, "Idle timeout for HTTP connections (e.g. 30s, 1m)")
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
		idleTimeout, _ := cmd.Flags().GetDuration("idle-timeout")
		prg := &mindMapService{addr: addr, dir: dir, webui: webuiDir, idleTimeout: idleTimeout}
		svc, err := service.New(prg, newServiceConfig(addr, dir, webuiDir, idleTimeout))
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

	// read idle-timeout for the non-service path
	idleTimeout, _ := cmd.Flags().GetDuration("idle-timeout")

	return runHTTPServer(addr, dir, webuiDir, idleTimeout, stopCh)
}

// runHTTPServer starts the HTTP/SSE server and blocks until stopCh is closed.
// Shared by both the interactive `serve` command and the system service.
func runHTTPServer(addr, dir, webuiDir string, idleTimeout time.Duration, stopCh chan struct{}) error {
	w, err := wiki.Open(dir)
	if err != nil {
		return fmt.Errorf("open wiki: %w", err)
	}
	defer w.Close()

	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Warn("failed to load config, using defaults", slog.Any("error", err))
		cfg = config.DefaultConfig()
	}
	_ = cfg // will be used by sync goroutine

	sseHandler := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		return mindmcp.NewServer(w).MCPServer()
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", sseHandler)

	// Settings API endpoints (UI only, not MCP)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		current, err := config.Load(cfgPath)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(current.Masked())
	})

	mux.HandleFunc("PUT /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "failed to read body", http.StatusBadRequest)
			return
		}

		// Load existing config to preserve token if masked
		existing, _ := config.Load(cfgPath)

		var incoming config.Config
		if err := json.Unmarshal(body, &incoming); err != nil {
			http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		// If the token is masked (unchanged from UI), keep the original
		if incoming.Sync.Token == "********" {
			incoming.Sync.Token = existing.Sync.Token
		}

		if err := config.Save(cfgPath, &incoming); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("settings saved", slog.String("path", cfgPath))
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(incoming.Masked())
	})

	mux.HandleFunc("POST /api/restart", func(rw http.ResponseWriter, r *http.Request) {
		slog.Info("restart requested via API")
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]string{"status": "restarting"})

		// Flush the response before restarting
		if f, ok := rw.(http.Flusher); ok {
			f.Flush()
		}

		logging.SafeGo("restart", func() {
			// Give the response time to reach the client
			time.Sleep(500 * time.Millisecond)

			// Graceful shutdown
			close(stopCh)
			time.Sleep(500 * time.Millisecond)

			// Self-exec restart
			exe, err := os.Executable()
			if err != nil {
				slog.Error("restart failed: cannot find executable", slog.Any("error", err))
				return
			}
			slog.Info("restarting", slog.String("exe", exe))
			syscall.Exec(exe, os.Args, os.Environ())
		})
	})

	mux.HandleFunc("GET /api/settings/path", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]string{"path": cfgPath})
	})

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
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       idleTimeout,
	}

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
