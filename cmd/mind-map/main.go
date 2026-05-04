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
	mindsync "github.com/aniongithub/mind-map/internal/sync"
	"github.com/aniongithub/mind-map/webui"
	"github.com/kardianos/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mind-map",
	Short: "A wiki engine with MCP interface for AI agents",
	Long:  "mind-map is a wiki that stores pages as markdown files, indexes them with SQLite FTS5, and exposes everything via MCP (stdio) or a REST API (serve). Agents use stdio, humans use the web UI.\n\nRunning without a subcommand starts the MCP server in stdio mode.",
	RunE:  runStdio,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server with web UI",
	Long:  "Starts the mind-map HTTP server with REST API and web UI.",
	RunE:  runServe,
}

func init() {
	rootCmd.PersistentFlags().StringP("dir", "d", defaultWikiDir(), "Path to the wiki directory")

	serveCmd.Flags().StringP("addr", "a", ":51849", "Address to listen on")
	serveCmd.Flags().String("webui", "", "Path to webui dist directory (overrides embedded webui)")
	serveCmd.Flags().String("log-file", "", "Path to log file (logs to stderr and file)")
	serveCmd.Flags().Duration("idle-timeout", 60*time.Second, "Idle timeout for HTTP connections (e.g. 30s, 1m)")
	serveCmd.Flags().Bool("run-as-service", false, "Run via kardianos/service (used by service manager)")
	serveCmd.Flags().MarkHidden("run-as-service")
	rootCmd.AddCommand(serveCmd)
}

// runStdio starts the MCP server in stdio mode (default).
func runStdio(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")

	w, err := wiki.Open(dir)
	if err != nil {
		return fmt.Errorf("open wiki: %w", err)
	}
	defer w.Close()

	s := mindmcp.NewServer(w, nil)
	slog.Info("mind-map MCP server starting", slog.String("mode", "stdio"), slog.String("wiki", w.Root()))
	return s.MCPServer().Run(cmd.Context(), &mcpsdk.StdioTransport{})
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
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

	// HTTP mode
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

// runHTTPServer starts the HTTP server and blocks until stopCh is closed.
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

	// Start git sync if enabled and configured
	var gs *mindsync.Manager
	if cfg.Sync.Enabled {
		remotes := cfg.Sync.Remotes()
		if len(remotes) > 0 {
			gs = mindsync.NewManager(w.Root(), cfgPath, cfg, w)
			if err := gs.Start(context.Background()); err != nil {
				slog.Error("failed to start sync", slog.Any("error", err))
				gs = nil
			} else {
				defer gs.Stop()
			}
		}
	}

	// REST API for wiki operations (used by web UI)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/context", func(rw http.ResponseWriter, r *http.Request) {
		wctx, err := w.Context(r.Context())
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(rw, wctx)
	})

	mux.HandleFunc("GET /api/pages", func(rw http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Query().Get("prefix")
		pages, err := w.ListPages(r.Context(), prefix)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(rw, pages)
	})

	mux.HandleFunc("GET /api/pages/{path...}", func(rw http.ResponseWriter, r *http.Request) {
		pagePath := r.PathValue("path")
		page, err := w.GetPage(r.Context(), pagePath)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		jsonResponse(rw, page)
	})

	mux.HandleFunc("POST /api/pages", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Path == "" || req.Content == "" {
			http.Error(rw, "path and content are required", http.StatusBadRequest)
			return
		}
		if err := w.CreatePage(r.Context(), req.Path, req.Content); err != nil {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		rw.WriteHeader(http.StatusCreated)
		jsonResponse(rw, map[string]string{"status": "created", "path": req.Path})
	})

	mux.HandleFunc("PUT /api/pages/{path...}", func(rw http.ResponseWriter, r *http.Request) {
		pagePath := r.PathValue("path")
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := w.UpdatePage(r.Context(), pagePath, req.Content); err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		jsonResponse(rw, map[string]string{"status": "updated", "path": pagePath})
	})

	mux.HandleFunc("DELETE /api/pages/{path...}", func(rw http.ResponseWriter, r *http.Request) {
		pagePath := r.PathValue("path")
		if err := w.DeletePage(r.Context(), pagePath); err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		jsonResponse(rw, map[string]string{"status": "deleted", "path": pagePath})
	})

	mux.HandleFunc("GET /api/search", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(rw, "q parameter is required", http.StatusBadRequest)
			return
		}
		results, err := w.Search(r.Context(), q, 20)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(rw, results)
	})

	mux.HandleFunc("GET /api/backlinks/{path...}", func(rw http.ResponseWriter, r *http.Request) {
		pagePath := r.PathValue("path")
		backlinks, err := w.GetBacklinks(r.Context(), pagePath)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(rw, backlinks)
	})

	// Settings API endpoints (UI only, not MCP)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		current, err := config.Load(cfgPath)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(current)
	})

	mux.HandleFunc("PUT /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "failed to read body", http.StatusBadRequest)
			return
		}

		var incoming config.Config
		if err := json.Unmarshal(body, &incoming); err != nil {
			http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Validate: if sync enabled, need at least a default or a mapping
		if incoming.Sync.Enabled && incoming.Sync.Default == "" && len(incoming.Sync.Mappings) == 0 {
			http.Error(rw, "sync requires at least a default remote or one mapping", http.StatusBadRequest)
			return
		}

		if err := config.Save(cfgPath, &incoming); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("settings saved", slog.String("path", cfgPath))
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(&incoming)
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

	mux.HandleFunc("GET /api/sync/status", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if gs != nil {
			json.NewEncoder(rw).Encode(gs.Status())
		} else {
			json.NewEncoder(rw).Encode(mindsync.Status{Enabled: false})
		}
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
		slog.String("url", "http://localhost"+addr),
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

func jsonResponse(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(v)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
