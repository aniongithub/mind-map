// Package share defines the pluggable export system for the mind-map wiki.
// Each export format (zip, PDF, HTML, etc.) implements the Sharer interface
// and registers itself in the global registry. The HTTP and MCP layers
// resolve a format name to a Sharer and delegate streaming.
package share

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// Page mirrors the subset of wiki.Page fields needed for export.
// Decoupled from the wiki package so the share package doesn't import it.
type Page struct {
	// Path relative to the wiki root, without extension.
	Path string
	// Title extracted from frontmatter or first heading.
	Title string
	// Body is the raw markdown content (without frontmatter).
	Body string
	// Frontmatter is the parsed YAML frontmatter as key-value pairs.
	Frontmatter map[string]interface{}
	// ModifiedAt is the file modification time.
	ModifiedAt time.Time
	// ImageRefs lists wiki-relative asset paths referenced by this page.
	ImageRefs []string
}

// AssetReader provides on-demand access to asset bytes by wiki-relative path.
type AssetReader interface {
	ReadAsset(ctx context.Context, path string) (content []byte, mime string, err error)
}

// SettingsField is a single configurable option within a SharerSettings.
type SettingsField struct {
	// Key is the machine name (e.g. "include_assets", "page_size").
	Key string `json:"key"`
	// Label is the human-readable display name.
	Label string `json:"label"`
	// Description is optional help text shown below the field.
	Description string `json:"description,omitempty"`
	// Type is one of: "bool", "int", "string", "enum".
	Type string `json:"type"`
	// Default is the default value.
	Default any `json:"default"`
	// Enum lists the allowed values when Type == "enum".
	Enum []string `json:"enum,omitempty"`
}

// SharerSettings describes the configurable knobs a share plugin exposes.
// Serialized as JSON; the web UI renders these as form fields.
type SharerSettings struct {
	// Fields is an ordered list of configuration fields the plugin accepts.
	Fields []SettingsField `json:"fields"`
}

// ShareConfig holds the user's choices for a specific export invocation.
type ShareConfig struct {
	// Format is the Sharer name to use (e.g. "zip").
	Format string `json:"format"`
	// Page is the starting page for link-graph traversal.
	Page string `json:"page"`
	// Depth controls how many wikilink hops to follow from the start page.
	// -1 = unlimited (all reachable pages), 0 = just this page,
	// 1 = this page + pages it links to, etc.
	Depth int `json:"depth"`
	// Settings holds the plugin-specific key-value pairs.
	// Keys correspond to SharerSettings.Fields[].Key.
	Settings map[string]any `json:"settings,omitempty"`
}

// ExportRequest is the fully-resolved bundle passed to Sharer.Export().
type ExportRequest struct {
	Config ShareConfig
	Pages  []Page
	Assets AssetReader
}

// Sharer is the interface every export plugin implements.
type Sharer interface {
	// Name returns the unique identifier for this format (e.g. "zip", "pdf").
	Name() string
	// Description returns a human-readable description for UI display.
	Description() string
	// Settings returns the schema for this plugin's configuration.
	Settings() SharerSettings
	// Export writes the exported content to w using the given request.
	Export(ctx context.Context, w io.Writer, req ExportRequest) error
	// ContentType returns the MIME type for HTTP responses.
	ContentType() string
	// FileExtension returns the file extension including the dot (e.g. ".zip").
	FileExtension() string
}

// FormatInfo is the JSON-serializable metadata about a registered format.
type FormatInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	ContentType string        `json:"content_type"`
	Extension   string        `json:"extension"`
	Settings    SharerSettings `json:"settings"`
}

// registry holds the global set of registered sharers.
var (
	registryMu sync.RWMutex
	registry   = make(map[string]Sharer)
)

// Register adds a Sharer to the global registry. Panics on duplicate name.
func Register(s Sharer) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := s.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("share: duplicate sharer registration: %q", name))
	}
	registry[name] = s
}

// Get returns the Sharer registered under the given name, or nil.
func Get(name string) Sharer {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// Formats returns metadata for all registered sharers.
func Formats() []FormatInfo {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]FormatInfo, 0, len(registry))
	for _, s := range registry {
		out = append(out, FormatInfo{
			Name:        s.Name(),
			Description: s.Description(),
			ContentType: s.ContentType(),
			Extension:   s.FileExtension(),
			Settings:    s.Settings(),
		})
	}
	return out
}

// SettingBool extracts a boolean setting from config, falling back to def.
func SettingBool(cfg ShareConfig, key string, def bool) bool {
	v, ok := cfg.Settings[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// SettingString extracts a string setting from config, falling back to def.
func SettingString(cfg ShareConfig, key string, def string) string {
	v, ok := cfg.Settings[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}
