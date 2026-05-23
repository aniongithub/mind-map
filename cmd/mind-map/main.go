package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"time"

	"github.com/aniongithub/mind-map/internal/config"
	"github.com/aniongithub/mind-map/internal/httpapi"
	"github.com/aniongithub/mind-map/internal/logging"
	mindmcp "github.com/aniongithub/mind-map/internal/mcp"
	"github.com/aniongithub/mind-map/internal/wiki"
	"github.com/aniongithub/mind-map/webui"
	"github.com/kardianos/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=vX.Y".
// Falls back to VCS info from runtime/debug for dev builds.
var version = ""

func getVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		rev := ""
		dirty := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) > 7 {
					rev = s.Value[:7]
				} else {
					rev = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			v := "dev (" + rev
			if dirty {
				v += ", dirty"
			}
			return v + ")"
		}
	}
	return "dev"
}

var rootCmd = &cobra.Command{
	Use:     "mind-map",
	Short:   "A wiki engine with MCP interface for AI agents",
	Long:    "mind-map is a wiki that stores pages as markdown files, indexes them with SQLite FTS5, and exposes everything via MCP (stdio) or a REST API (serve). Agents use stdio, humans use the web UI.\n\nRunning without a subcommand starts the MCP server in stdio mode.",
	Version: getVersion(),
	RunE:    runStdio,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server with web UI",
	Long:  "Starts the mind-map HTTP server with REST API and web UI.",
	RunE:  runServe,
}

func init() {
	rootCmd.PersistentFlags().StringP("dir", "d", defaultWikiDir(), "Path to the wiki directory")

	serveCmd.Flags().StringP("addr", "a", "127.0.0.1:4242", "Address to listen on")
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

	s := mindmcp.NewServer(w, nil, getVersion())
	slog.Info("mind-map MCP server starting", slog.String("mode", "stdio"), slog.String("wiki", w.Root()))
	return s.MCPServer().Run(cmd.Context(), &mcpsdk.StdioTransport{})
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	logFile, _ := cmd.Flags().GetString("log-file")
	runAsService, _ := cmd.Flags().GetBool("run-as-service")

	if runAsService {
		// Launched by the OS service manager — delegate to kardianos/service.
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

	// Interactive mode: stderr + optional file logging.
	if f := logging.Init(nil, logFile); f != nil {
		defer f.Close()
	}

	addr, _ := cmd.Flags().GetString("addr")
	webuiDir, _ := cmd.Flags().GetString("webui")
	idleTimeout, _ := cmd.Flags().GetDuration("idle-timeout")

	stopCh := make(chan struct{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		slog.Info("received interrupt, shutting down")
		// stopCh may already be closed (e.g. by /api/restart); guard.
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}()

	return runHTTPServer(addr, dir, webuiDir, idleTimeout, stopCh)
}

// runHTTPServer wires the HTTP handler from internal/httpapi and serves it.
// Shared by the interactive `serve` command and the system service.
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

	handler := httpapi.New(httpapi.Deps{
		Wiki:       w,
		CfgPath:    cfgPath,
		Cfg:        cfg,
		GetVersion: getVersion,
		StopCh:     stopCh,
		WebFS:      resolveWebFS(webuiDir),
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       idleTimeout,
	}

	slog.Info("mind-map server starting",
		slog.String("addr", addr),
		slog.String("wiki", w.Root()),
		slog.String("url", "http://"+addr),
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

// resolveWebFS picks the embedded SPA filesystem unless an override directory
// is provided and exists. Returns nil when no UI is available; the httpapi
// package serves a "not built" placeholder in that case.
func resolveWebFS(webuiDir string) fs.FS {
	if webuiDir != "" {
		if _, err := os.Stat(webuiDir); err == nil {
			return os.DirFS(webuiDir)
		}
	}
	return webui.DistFS()
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
