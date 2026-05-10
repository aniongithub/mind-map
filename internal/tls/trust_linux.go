package tls

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

// InstallCA installs the CA certificate in the system trust store.
// On Linux this uses update-ca-certificates (Debian/Ubuntu) or
// update-ca-trust (RHEL/Fedora).
func InstallCA(dir string) error {
	caCert := CACertPath(dir)

	// Debian/Ubuntu
	if dest := "/usr/local/share/ca-certificates/mind-map-ca.crt"; true {
		if err := copyFile(caCert, dest); err == nil {
			if cmd := exec.Command("update-ca-certificates"); cmd != nil {
				if out, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("update-ca-certificates: %s: %w", out, err)
				}
				return nil
			}
		}
	}

	// RHEL/Fedora
	if dest := "/etc/pki/ca-trust/source/anchors/mind-map-ca.crt"; true {
		if err := copyFile(caCert, dest); err == nil {
			if cmd := exec.Command("update-ca-trust"); cmd != nil {
				if out, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("update-ca-trust: %s: %w", out, err)
				}
				return nil
			}
		}
	}

	return fmt.Errorf("could not install CA cert: no supported trust store found")
}

// UninstallCA removes the CA certificate from the system trust store.
func UninstallCA(_ string) error {
	// Debian/Ubuntu
	debPath := "/usr/local/share/ca-certificates/mind-map-ca.crt"
	if removeIfExists(debPath) {
		exec.Command("update-ca-certificates", "--fresh").CombinedOutput()
		return nil
	}

	// RHEL/Fedora
	rhelPath := "/etc/pki/ca-trust/source/anchors/mind-map-ca.crt"
	if removeIfExists(rhelPath) {
		exec.Command("update-ca-trust").CombinedOutput()
		return nil
	}

	return nil
}

func copyFile(src, dst string) error {
	out, err := exec.Command("cp", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp %s %s: %s: %w", filepath.Base(src), dst, out, err)
	}
	return nil
}
