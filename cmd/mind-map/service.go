package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// mindMapService implements service.Interface for kardianos/service.
type mindMapService struct {
	addr    string
	dir     string
	webui   string
	stopCh  chan struct{}
	errCh   chan error
}

func (m *mindMapService) Start(s service.Service) error {
	m.stopCh = make(chan struct{})
	m.errCh = make(chan error, 1)
	go func() {
		m.errCh <- runHTTPServer(m.addr, m.dir, m.webui, m.stopCh)
	}()
	return nil
}

func (m *mindMapService) Stop(s service.Service) error {
	if m.stopCh != nil {
		close(m.stopCh)
	}
	return nil
}

func defaultWikiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mind-map", "wiki")
}

func newServiceConfig(addr, dir, webui string) *service.Config {
	execPath, _ := os.Executable()
	args := []string{"serve", "--addr", addr, "--dir", dir}
	if webui != "" {
		args = append(args, "--webui", webui)
	}

	cfg := &service.Config{
		Name:        "mind-map",
		DisplayName: "mind-map",
		Description: "mind-map wiki server — MCP over HTTP/SSE",
		Arguments:   args,
		Executable:  execPath,
	}

	// On macOS, install as a user agent (no root required)
	if runtime.GOOS == "darwin" {
		cfg.Option = service.KeyValue{
			"UserService": true,
		}
	}

	return cfg
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the mind-map system service",
	Long:  "Install, start, stop, or uninstall mind-map as a system service (Windows Service, systemd, or launchd).",
}

func init() {
	// Shared flags for service subcommands that need them
	for _, cmd := range []*cobra.Command{serviceInstallCmd, serviceStartCmd, serviceStopCmd, serviceUninstallCmd, serviceStatusCmd} {
		cmd.Flags().StringP("addr", "a", ":51849", "Address to listen on")
		cmd.Flags().StringP("dir", "d", defaultWikiDir(), "Path to the wiki directory")
		cmd.Flags().String("webui", "", "Path to webui dist directory (overrides embedded)")
	}

	serviceCmd.AddCommand(serviceInstallCmd, serviceStartCmd, serviceStopCmd, serviceUninstallCmd, serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install mind-map as a system service",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		dir, _ := cmd.Flags().GetString("dir")
		webui, _ := cmd.Flags().GetString("webui")

		// Ensure wiki directory exists
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create wiki dir: %w", err)
		}

		svc, err := service.New(&mindMapService{}, newServiceConfig(addr, dir, webui))
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		if err := svc.Install(); err != nil {
			return fmt.Errorf("install service: %w", err)
		}
		fmt.Println("Service installed.")
		fmt.Printf("  Wiki:     %s\n", dir)
		fmt.Printf("  Address:  %s\n", addr)
		fmt.Println()
		fmt.Println("Start it with: mind-map service start")
		return nil
	},
}

var serviceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the mind-map service",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		dir, _ := cmd.Flags().GetString("dir")
		webui, _ := cmd.Flags().GetString("webui")

		svc, err := service.New(&mindMapService{}, newServiceConfig(addr, dir, webui))
		if err != nil {
			return err
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		fmt.Println("Service started.")
		fmt.Printf("  Web UI:       http://localhost%s\n", addr)
		fmt.Printf("  MCP endpoint: http://localhost%s/mcp\n", addr)
		return nil
	},
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the mind-map service",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		dir, _ := cmd.Flags().GetString("dir")
		webui, _ := cmd.Flags().GetString("webui")

		svc, err := service.New(&mindMapService{}, newServiceConfig(addr, dir, webui))
		if err != nil {
			return err
		}
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		fmt.Println("Service stopped.")
		return nil
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall the mind-map service",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		dir, _ := cmd.Flags().GetString("dir")
		webui, _ := cmd.Flags().GetString("webui")

		svc, err := service.New(&mindMapService{}, newServiceConfig(addr, dir, webui))
		if err != nil {
			return err
		}
		// Stop first, ignore errors (might not be running)
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("uninstall service: %w", err)
		}
		fmt.Println("Service uninstalled.")
		return nil
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the mind-map service status",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		dir, _ := cmd.Flags().GetString("dir")
		webui, _ := cmd.Flags().GetString("webui")

		svc, err := service.New(&mindMapService{}, newServiceConfig(addr, dir, webui))
		if err != nil {
			return err
		}
		status, err := svc.Status()
		if err != nil {
			return fmt.Errorf("query status: %w", err)
		}
		switch status {
		case service.StatusRunning:
			fmt.Println("Service is running.")
		case service.StatusStopped:
			fmt.Println("Service is stopped.")
		default:
			fmt.Println("Service status: unknown.")
		}
		return nil
	},
}
