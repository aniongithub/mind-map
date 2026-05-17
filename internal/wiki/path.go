package wiki

import (
	"fmt"
	"path"
	"strings"
)

// normalizePagePath canonicalizes an externally-supplied page path so that
// equivalent denormalized inputs map to the same primary key in the index.
//
// Rules:
//   - backslashes are converted to forward slashes (defensive — Windows agents)
//   - a single trailing ".md" extension is stripped (the API contract is
//     "path without .md extension", but be forgiving)
//   - the path is run through path.Clean to collapse "//", "./", trailing "/"
//   - a leading "/" or "./" is removed (page paths are wiki-root-relative)
//   - paths that escape the wiki root ("..", "../foo", ...) are rejected
//   - empty, ".", and "/" are rejected
func normalizePagePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("page path is empty")
	}

	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimSuffix(p, ".md")

	cleaned := path.Clean(p)
	cleaned = strings.TrimPrefix(cleaned, "/")

	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("invalid page path: %q", p)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("page path escapes wiki root: %q", p)
	}

	return cleaned, nil
}
