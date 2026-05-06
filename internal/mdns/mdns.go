// Package mdns registers mind-map as an mDNS service so it is
// discoverable on the local network as "mind-map.local".
package mdns

import (
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/hashicorp/mdns"
)

// Server wraps a hashicorp/mdns server for clean shutdown.
type Server struct {
	inner *mdns.Server
}

// Register advertises mind-map as an _http._tcp mDNS service on the
// given port. The hostname is set to "mind-map.local." so that
// browsers can reach the server at http://mind-map.local.
//
// Returns a *Server whose Shutdown method must be called on exit.
func Register(port int) (*Server, error) {
	host, _ := os.Hostname()

	ips := localIPs()
	if len(ips) == 0 {
		return nil, fmt.Errorf("mdns: no usable network interfaces found")
	}

	svc, err := mdns.NewMDNSService(
		"mind-map",      // instance name
		"_http._tcp",    // service type
		"",              // domain — defaults to "local."
		"mind-map.local.", // hostname
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

	slog.Info("mDNS registered",
		slog.String("hostname", "mind-map.local"),
		slog.Int("port", port),
		slog.Any("ips", ips),
	)

	return &Server{inner: srv}, nil
}

// Shutdown stops the mDNS server and deregisters the service.
func (s *Server) Shutdown() {
	if s != nil && s.inner != nil {
		s.inner.Shutdown()
		slog.Info("mDNS deregistered")
	}
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
