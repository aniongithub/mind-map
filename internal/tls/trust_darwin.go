package tls

import (
	"fmt"
	"os/exec"
)

// InstallCA installs the CA certificate in the macOS System Keychain.
func InstallCA(dir string) error {
	caCert := CACertPath(dir)
	out, err := exec.Command("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		caCert,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %s: %w", out, err)
	}
	return nil
}

// UninstallCA removes the CA certificate from the macOS System Keychain.
func UninstallCA(_ string) error {
	out, err := exec.Command("security", "delete-certificate",
		"-c", "mind-map Local CA",
		"-t", "/Library/Keychains/System.keychain",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("security delete-certificate: %s: %w", out, err)
	}
	return nil
}
