//go:build !linux && !darwin && !windows

package mdns

import "fmt"

// registerSystem is not supported on this platform; the caller
// will fall back to the built-in mDNS responder.
func registerSystem(_ int) (Registration, error) {
	return nil, fmt.Errorf("no system mDNS responder available on this platform")
}
