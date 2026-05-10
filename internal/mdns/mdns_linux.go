package mdns

import (
	"fmt"
	"log/slog"
	"net"
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
	if _, err := exec.LookPath("avahi-publish-address"); err != nil {
		return nil, fmt.Errorf("avahi-publish-address not found: %w", err)
	}

	ip := primaryIPv4()
	if ip == "" {
		return nil, fmt.Errorf("no usable IPv4 address found")
	}

	// Publish the address record: mind-map.local → <IP>
	addrCmd := exec.Command("avahi-publish-address", "--no-fail", "mind-map.local", ip)
	addrCmd.Stdout = nil
	addrCmd.Stderr = nil
	if err := addrCmd.Start(); err != nil {
		return nil, fmt.Errorf("avahi-publish-address: %w", err)
	}

	// Publish the service record: mind-map._http._tcp on the given port
	svcCmd := exec.Command("avahi-publish-service",
		"--no-fail",
		"--host=mind-map.local",
		"mind-map",
		"_http._tcp",
		strconv.Itoa(port),
	)
	svcCmd.Stdout = nil
	svcCmd.Stderr = nil
	if err := svcCmd.Start(); err != nil {
		// Clean up the address publisher
		addrCmd.Process.Kill()
		addrCmd.Wait()
		return nil, fmt.Errorf("avahi-publish-service: %w", err)
	}

	slog.Info("mDNS registered (avahi)",
		slog.String("hostname", "mind-map.local"),
		slog.String("ip", ip),
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

// primaryIPv4 returns the first non-loopback IPv4 address from a
// non-virtual interface (skips docker/veth bridges).
func primaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip common virtual/container interfaces
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}

func isVirtualInterface(name string) bool {
	prefixes := []string{"docker", "br-", "veth", "virbr", "lxc", "cni", "flannel", "calico"}
	for _, p := range prefixes {
		if len(name) >= len(p) && name[:len(p)] == p {
			return true
		}
	}
	return false
}
