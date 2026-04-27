package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var embeddedFS embed.FS

// DistFS returns the embedded webui/dist filesystem.
// Returns nil if the webui was not built at compile time.
func DistFS() fs.FS {
	entries, err := fs.ReadDir(embeddedFS, "dist")
	if err != nil || len(entries) == 0 || (len(entries) == 1 && entries[0].Name() == ".gitkeep") {
		return nil
	}
	sub, err := fs.Sub(embeddedFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}
