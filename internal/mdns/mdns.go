// Package mdns registers mind-map as an mDNS service so it is
// discoverable on the local network as "mind-map.local".
//
// On Linux it registers through avahi-daemon (avahi-publish-service),
// on macOS through Bonjour (dns-sd). Both avoid conflicts with the
// system mDNS responder. If neither is available, it falls back to
// a pure-Go mDNS responder (hashicorp/mdns).
package mdns

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"

	"github.com/hashicorp/mdns"
)

// Registration represents an active mDNS registration that can be shut down.
type Registration interface {
	Shutdown()
}

// Register advertises mind-map as an _http._tcp mDNS service on the
// given port. It tries the system mDNS responder first (avahi on Linux,
// dns-sd on macOS) and falls back to a built-in responder.
//
// Returns a Registration whose Shutdown method must be called on exit.
func Register(port int) (Registration, error) {
	// Try system mDNS first
	reg, err := registerSystem(port)
	if err == nil {
		return reg, nil
	}
	slog.Info("system mDNS not available, using built-in responder", slog.Any("reason", err))

	return registerFallback(port)
}

// --- system registration (avahi-publish-service / dns-sd) ---

// systemRegistration wraps an os/exec.Cmd subprocess.
type systemRegistration struct {
	cmd *exec.Cmd
}

func (s *systemRegistration) Shutdown() {
	if s != nil && s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		slog.Info("mDNS deregistered (system)")
	}
}

// --- built-in fallback (hashicorp/mdns) ---

// fallbackRegistration wraps the hashicorp/mdns server.
type fallbackRegistration struct {
	inner *mdns.Server
}

func (f *fallbackRegistration) Shutdown() {
	if f != nil && f.inner != nil {
		f.inner.Shutdown()
		slog.Info("mDNS deregistered (built-in)")
	}
}

func registerFallback(port int) (Registration, error) {
	host, _ := os.Hostname()

	ips := localIPs()
	if len(ips) == 0 {
		return nil, fmt.Errorf("mdns: no usable network interfaces found")
	}

	svc, err := mdns.NewMDNSService(
		"mind-map",
		"_http._tcp",
		"",
		"mind-map.local.",
		port,
		ips,
		[]string{fmt.Sprintf("path=/"), fmt.Sprintf("host=%s", host)},
	)
	if err != nil {
		return nil, fmt.Errorf("mdns: create service: %w", err)
	}

	srv, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		return nil, fmt.Errorf("mdns: start server: %w", err)
	}

	slog.Info("mDNS registered (built-in)",
		slog.String("hostname", "mind-map.local"),
		slog.Int("port", port),
	)

	return &fallbackRegistration{inner: srv}, nil
}

// startPublisher launches a long-running mDNS publishing command and
// returns a systemRegistration. The caller must call Shutdown to kill it.
func startPublisher(name string, args ...string) (Registration, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	portStr := ""
	for i, a := range args {
		if i > 0 {
			portStr = a // last positional arg is port in both avahi/dns-sd
		}
	}
	port, _ := strconv.Atoi(portStr)

	slog.Info("mDNS registered (system)",
		slog.String("hostname", "mind-map.local"),
		slog.String("tool", name),
		slog.Int("port", port),
	)

	return &systemRegistration{cmd: cmd}, nil
}

// localIPs returns all non-loopback IPv4 and IPv6 addresses.
func localIPs() []net.IP {
	var ips []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ips = append(ips, ipNet.IP)
	}
	return ips
}
