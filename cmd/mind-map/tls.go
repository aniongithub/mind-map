package main

import (
	"fmt"

	mindtls "github.com/aniongithub/mind-map/internal/tls"
	"github.com/spf13/cobra"
)

var tlsCmd = &cobra.Command{
	Use:   "tls",
	Short: "Manage TLS certificates for HTTPS",
	Long:  "Generate and install a local CA so mind-map can serve HTTPS on mind-map.local without browser warnings.",
}

var tlsSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Generate certs and install CA in system trust store",
	Long:  "Generates certificates then installs the CA in the system trust store. On Linux, the CA install step requires sudo — use 'tls generate' + 'sudo mind-map tls install-ca' separately if needed.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("tls-dir")

		fmt.Println("==> Generating TLS certificates...")
		if err := mindtls.Generate(dir); err != nil {
			return fmt.Errorf("generate certs: %w", err)
		}
		fmt.Printf("    Certificates written to %s\n", dir)

		fmt.Println("==> Installing CA in system trust store...")
		if err := mindtls.InstallCA(dir); err != nil {
			return fmt.Errorf("install CA: %w", err)
		}
		fmt.Println("    CA installed. Browsers will trust mind-map.local.")

		fmt.Println()
		fmt.Println("Done! Restart the mind-map service to enable HTTPS.")
		return nil
	},
}

var tlsGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate TLS certificates only (no trust store install)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("tls-dir")

		fmt.Println("==> Generating TLS certificates...")
		if err := mindtls.Generate(dir); err != nil {
			return fmt.Errorf("generate certs: %w", err)
		}
		fmt.Printf("    Certificates written to %s\n", dir)
		fmt.Println()
		fmt.Println("Run 'sudo mind-map tls install-ca --tls-dir " + dir + "' to trust the CA.")
		return nil
	},
}

var tlsInstallCACmd = &cobra.Command{
	Use:   "install-ca",
	Short: "Install the CA certificate in the system trust store",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("tls-dir")

		if !mindtls.HasCerts(dir) {
			return fmt.Errorf("no certificates found in %s — run 'mind-map tls generate' first", dir)
		}

		fmt.Println("==> Installing CA in system trust store...")
		if err := mindtls.InstallCA(dir); err != nil {
			return fmt.Errorf("install CA: %w", err)
		}
		fmt.Println("    CA installed. Browsers will trust mind-map.local.")
		return nil
	},
}

var tlsRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove certs and uninstall CA from trust store",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("tls-dir")

		fmt.Println("==> Removing CA from system trust store...")
		if err := mindtls.UninstallCA(dir); err != nil {
			fmt.Printf("    Warning: %v\n", err)
		}

		fmt.Println("==> Removing TLS certificates...")
		mindtls.Remove(dir)

		fmt.Println("Done! Restart the mind-map service to switch back to HTTP.")
		return nil
	},
}

func init() {
	// Add --tls-dir flag to all tls subcommands
	for _, cmd := range []*cobra.Command{tlsSetupCmd, tlsGenerateCmd, tlsInstallCACmd, tlsRemoveCmd} {
		cmd.Flags().String("tls-dir", mindtls.DefaultDir(), "Path to TLS certificate directory")
	}
	tlsCmd.AddCommand(tlsSetupCmd, tlsGenerateCmd, tlsInstallCACmd, tlsRemoveCmd)
	rootCmd.AddCommand(tlsCmd)
}
