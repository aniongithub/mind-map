package tls

import "os"

func removeIfExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		os.Remove(path)
		return true
	}
	return false
}
