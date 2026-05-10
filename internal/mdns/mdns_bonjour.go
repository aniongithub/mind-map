//go:build darwin || windows

package mdns

import (
	"fmt"
	"os/exec"
	"strconv"
)

// registerSystem uses dns-sd (Bonjour) to register through the
// system's mDNSResponder, avoiding conflicts with the OS resolver.
// On macOS dns-sd is always present; on Windows it is available
// when Bonjour (via iTunes, Bonjour Print Services, etc.) is installed.
//
//   dns-sd -R "mind-map" _http._tcp local 51888
func registerSystem(port int) (Registration, error) {
	path, err := exec.LookPath("dns-sd")
	if err != nil {
		return nil, fmt.Errorf("dns-sd not found: %w", err)
	}

	return startPublisher(path,
		"-R",
		"mind-map",
		"_http._tcp",
		"local",
		strconv.Itoa(port),
	)
}
