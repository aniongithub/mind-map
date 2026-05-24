package wiki

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

var md = goldmark.New(
	goldmark.WithExtensions(
		meta.Meta,
	),
)

// parsedPage holds the result of parsing a markdown file's raw bytes.
type parsedPage struct {
	title       string
	body        string
	frontmatter map[string]interface{}
	links       []string
	// images holds standard markdown image destinations (`![](path)`)
	// in document order, deduplicated. These are tracked in the links
	// table with kind='image' so lifecycle operations (delete/move/GC)
	// can run as plain index queries.
	images []string
}

// parsePage extracts frontmatter, title, body text, wikilinks, and image
// references from raw markdown.
func parsePage(raw []byte) parsedPage {
	ctx := parser.NewContext()
	reader := text.NewReader(raw)

	// Parse fully now: we use the AST to extract image destinations
	// (standard markdown ![](path)). Wikilinks are still string-scanned
	// from the post-frontmatter body since they're a non-standard
	// extension and goldmark doesn't recognize them.
	doc := md.Parser().Parse(reader, parser.WithContext(ctx))

	fm := meta.Get(ctx)
	body := stripFrontmatter(raw)

	return parsedPage{
		title:       extractTitle(fm, body),
		body:        string(body),
		frontmatter: fm,
		links:       extractWikilinks(body),
		images:      extractImages(doc, raw),
	}
}

// stripFrontmatter removes the YAML frontmatter block from raw markdown
// and returns the body bytes.
func stripFrontmatter(raw []byte) []byte {
	s := string(raw)
	if !strings.HasPrefix(s, "---") {
		return raw
	}
	end := strings.Index(s[3:], "---")
	if end < 0 {
		return raw
	}
	offset := 3 + end + 3
	// Skip the trailing newline after closing ---
	if offset < len(s) && s[offset] == '\n' {
		offset++
	}
	return []byte(s[offset:])
}

// extractTitle returns the title from the frontmatter "title" field, or the
// first markdown `# heading`, or "" if neither is present. Callers are
// responsible for any filename fallback.
func extractTitle(fm map[string]interface{}, body []byte) string {
	if fm != nil {
		if t, ok := fm["title"]; ok {
			if s, ok := t.(string); ok && s != "" {
				return s
			}
		}
	}

	// Look for first # heading
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(trimmed[2:])
		}
	}

	return ""
}

// extractWikilinks finds all [[target]] patterns in markdown text.
// Returns deduplicated target strings.
func extractWikilinks(body []byte) []string {
	s := string(body)
	seen := make(map[string]bool)
	var links []string

	for {
		start := strings.Index(s, "[[")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "]]")
		if end < 0 {
			break
		}
		target := strings.TrimSpace(s[start+2 : start+end])
		// Handle [[display|target]] syntax
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = strings.TrimSpace(target[pipe+1:])
		}
		if target != "" && !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
		s = s[start+end+2:]
	}

	return links
}

// extractImages walks the markdown AST and returns the destinations of
// every `![](path)` image in document order, deduplicated. External URLs
// (anything with a scheme) are skipped — we only track wiki-local
// references because those are the ones the lifecycle code needs to
// reason about. Anchor-only refs (`#foo`) and empty destinations are
// also skipped.
//
// The `raw` parameter is the full file bytes (including frontmatter);
// goldmark uses byte offsets into this when reporting node positions,
// but ast.Image carries its destination directly so we don't need to
// re-slice the source.
func extractImages(doc ast.Node, _ []byte) []string {
	seen := make(map[string]bool)
	var images []string

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		img, ok := n.(*ast.Image)
		if !ok {
			return ast.WalkContinue, nil
		}
		dest := string(img.Destination)
		if !isWikiLocalRef(dest) {
			return ast.WalkContinue, nil
		}
		if !seen[dest] {
			seen[dest] = true
			images = append(images, dest)
		}
		return ast.WalkContinue, nil
	})

	return images
}

// isWikiLocalRef reports whether a markdown image destination points at a
// wiki-local asset rather than an external resource. External resources
// (http://, https://, data:, mailto:, etc.) are intentionally ignored —
// the lifecycle code only manages files inside the wiki tree.
func isWikiLocalRef(dest string) bool {
	if dest == "" {
		return false
	}
	if strings.HasPrefix(dest, "#") {
		return false
	}
	// Reject anything with a URL scheme (RFC 3986 scheme is
	// ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ) followed by ":").
	// A bare check for "://" misses `data:` and `mailto:`, hence the
	// stricter scan: first colon before any slash means scheme.
	for i := 0; i < len(dest); i++ {
		c := dest[i]
		if c == ':' {
			return false
		}
		if c == '/' || c == '?' || c == '#' {
			break
		}
	}
	return true
}
