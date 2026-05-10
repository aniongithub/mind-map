package tls

import (
	"fmt"
	"os/exec"
)

// InstallCA installs the CA certificate in the Windows Root trust store.
func InstallCA(dir string) error {
	caCert := CACertPath(dir)
	out, err := exec.Command("certutil", "-addstore", "-f", "Root", caCert).CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -addstore: %s: %w", out, err)
	}
	return nil
}

// UninstallCA removes the CA certificate from the Windows Root trust store.
func UninstallCA(_ string) error {
	out, err := exec.Command("certutil", "-delstore", "Root", "mind-map Local CA").CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -delstore: %s: %w", out, err)
	}
	return nil
}
