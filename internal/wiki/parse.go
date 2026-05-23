package wiki

import (
	"strings"

	"github.com/yuin/goldmark"
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
}

// parsePage extracts frontmatter, title, body text, and wikilinks from raw markdown.
func parsePage(raw []byte) parsedPage {
	ctx := parser.NewContext()
	reader := text.NewReader(raw)

	// Parse only to populate the meta context; goldmark-meta stores the
	// YAML frontmatter map on ctx as a side effect. The AST itself is
	// unused here — we extract the body via stripFrontmatter below.
	md.Parser().Parse(reader, parser.WithContext(ctx))

	fm := meta.Get(ctx)
	body := stripFrontmatter(raw)

	return parsedPage{
		title:       extractTitle(fm, body),
		body:        string(body),
		frontmatter: fm,
		links:       extractWikilinks(body),
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
