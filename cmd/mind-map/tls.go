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
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := mindtls.DefaultDir()

		fmt.Println("==> Generating TLS certificates...")
		if err := mindtls.Generate(dir); err != nil {
			return fmt.Errorf("generate certs: %w", err)
		}
		fmt.Printf("    Certificates written to %s\n", dir)

		fmt.Println("==> Installing CA in system trust store (may require sudo)...")
		if err := mindtls.InstallCA(dir); err != nil {
			return fmt.Errorf("install CA: %w", err)
		}
		fmt.Println("    CA installed. Browsers will trust mind-map.local.")

		fmt.Println()
		fmt.Println("Done! Restart the mind-map service to enable HTTPS.")
		return nil
	},
}

var tlsRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove certs and uninstall CA from trust store",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := mindtls.DefaultDir()

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
	tlsCmd.AddCommand(tlsSetupCmd, tlsRemoveCmd)
	rootCmd.AddCommand(tlsCmd)
}
