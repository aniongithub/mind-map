package mdns

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
)

// registerSystem uses avahi-publish-address and avahi-publish-service
// to register through the system's avahi-daemon, avoiding port 5353
// conflicts with the built-in responder.
func registerSystem(port int) (Registration, error) {
	if _, err := exec.LookPath("avahi-publish-service"); err != nil {
		return nil, fmt.Errorf("avahi-publish-service not found: %w", err)
	}

	// Publish the service record: mind-map._http._tcp on the given port.
	// Don't set --host so it uses the machine's real hostname (always resolvable).
	svcCmd := exec.Command("avahi-publish-service",
		"--no-fail",
		"mind-map",
		"_http._tcp",
		strconv.Itoa(port),
	)
	svcCmd.Stdout = nil
	svcCmd.Stderr = nil
	if err := svcCmd.Start(); err != nil {
		return nil, fmt.Errorf("avahi-publish-service: %w", err)
	}

	// Best-effort: publish address record mind-map.local → 127.0.0.1.
	// Binds to loopback so the wiki is only accessible on this machine.
	// This may fail (e.g. name collision) — that's OK, the service
	// record above is the important one.
	var addrCmd *exec.Cmd
	if path, err := exec.LookPath("avahi-publish-address"); err == nil {
		addrCmd = exec.Command(path, "--no-fail", "mind-map.local", "127.0.0.1")
		addrCmd.Stdout = nil
		addrCmd.Stderr = nil
		if err := addrCmd.Start(); err != nil {
			slog.Warn("avahi-publish-address failed to start", slog.Any("error", err))
			addrCmd = nil
		}
	}

	slog.Info("mDNS registered (avahi)",
		slog.String("hostname", "mind-map.local"),
		slog.Int("port", port),
	)

	return &avahiRegistration{addr: addrCmd, svc: svcCmd}, nil
}

// avahiRegistration holds both avahi-publish subprocesses.
type avahiRegistration struct {
	addr *exec.Cmd
	svc  *exec.Cmd
}

func (a *avahiRegistration) Shutdown() {
	if a == nil {
		return
	}
	for _, cmd := range []*exec.Cmd{a.svc, a.addr} {
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	slog.Info("mDNS deregistered (avahi)")
}
