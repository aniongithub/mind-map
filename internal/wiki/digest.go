package wiki

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// AreaSummary is one entry under `## Areas` in the rendered digest:
// a top-level directory, how many pages live under it, and (if the
// directory has an `index.md`) that index page's title as a one-line
// description.
type AreaSummary struct {
	Path        string `json:"path"`
	PageCount   int    `json:"page_count"`
	IndexTitle  string `json:"index_title,omitempty"`
}

// Digest is the structured form of the per-conversation orientation
// blob. The MCP `get_wiki_digest` tool and HTTP `/api/digest` endpoint
// return this — the markdown is what an LLM consumes; the typed fields
// let the WebUI render its own views (e.g. a word-cloud widget) without
// re-parsing the markdown.
type Digest struct {
	PageCount int           `json:"page_count"`
	Cloud     []CloudTerm   `json:"cloud_terms"`
	Recents   []string      `json:"recents"`
	Areas     []AreaSummary `json:"areas"`
	Markdown  string        `json:"markdown"`
}

// defaultMaxRenderBytes is the soft cap on the rendered markdown.
// Trim order when over: recents -> cloud -> areas (never). Matches
// the plan's ~4 KB target. Tunable via config (Step 7).
const defaultMaxRenderBytes = 4096

// digestCache is a single-slot cache for the rendered Digest, keyed
// by the (cloud version, recents seq) tuple at render time. The
// digest itself is a few-hundred-byte structure; what we're saving
// is the SQL roundtrip for area counts and the render loop, not the
// allocation.
type digestCache struct {
	mu          sync.Mutex
	cloudVer    uint64
	recentsSeq  uint64
	pageCount   int
	cached      *Digest
}

// get returns the cached digest if (cloudVer, recentsSeq, pageCount)
// match the supplied values. pageCount is part of the key because a
// page added or removed without touching the LRU (rare, but happens
// on reindex for pure-content-change pages) still changes the header
// sentence ("This wiki contains N pages...").
//
// Returns (nil, false) on a miss.
func (c *digestCache) get(cloudVer, recentsSeq uint64, pageCount int) (*Digest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil {
		return nil, false
	}
	if c.cloudVer != cloudVer || c.recentsSeq != recentsSeq || c.pageCount != pageCount {
		return nil, false
	}
	return c.cached, true
}

func (c *digestCache) set(cloudVer, recentsSeq uint64, pageCount int, d *Digest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cloudVer = cloudVer
	c.recentsSeq = recentsSeq
	c.pageCount = pageCount
	c.cached = d
}

// invalidate clears the cache. Used in tests and on schema rebuilds
// (Step 4); CRUD doesn't need to call this because version bumps
// already cover the cache invalidation contract.
func (c *digestCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = nil
}

// Digest returns the current orientation digest. Cheap on cache hit;
// on miss, builds in O(pages) for the area counts and O(K) for the
// render. Safe for concurrent callers.
//
// This is the function HTTP `/api/digest` and the MCP `get_wiki_digest`
// tool call. It is also called transitively from the existing
// `get_wiki_context` (see Step 5) so old clients see the new data
// shape without breakage.
func (w *Wiki) Digest(ctx context.Context) (*Digest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pageCount, err := w.pageCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("digest page count: %w", err)
	}

	cloudVer := w.cloud.Version()
	recentsSeq := w.recents.version()

	if d, ok := w.digest.get(cloudVer, recentsSeq, pageCount); ok {
		return d, nil
	}

	areas, err := w.areaSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("digest areas: %w", err)
	}

	cloudTerms, _ := w.cloud.Get() // ok == false → empty, render copes
	recents := w.recents.snapshot()

	d := &Digest{
		PageCount: pageCount,
		Cloud:     cloudTerms,
		Recents:   recents,
		Areas:     areas,
	}
	d.Markdown = renderDigestMarkdown(d, w.renderCap())

	w.digest.set(cloudVer, recentsSeq, pageCount, d)
	return d, nil
}

// renderCap returns the effective byte cap to pass into the markdown
// renderer. Normalized in Open() to:
//
//	> 0  → trim to that size
//	== 0 → defaulted, never observed here
//	< 0  → no trimming
//
// The renderer treats <= 0 uniformly as "no trim," so we forward
// negative values straight through.
func (w *Wiki) renderCap() int {
	return w.maxRenderBytes
}

// pageCount runs the same SELECT COUNT(*) the Context handler uses.
// Lifted into a helper so Digest can share it.
func (w *Wiki) pageCount(ctx context.Context) (int, error) {
	var n int
	if err := w.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pages").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// areaSummaries returns one entry per top-level directory in the wiki,
// with the page count and (if the directory has `<area>/index.md`)
// the title of that index page. Sorted by descending page count, then
// by name — same shape as the rendered markdown.
//
// An area with no pages under it cannot exist (the source data is the
// `pages` table; empty dirs aren't tracked). A flat-rooted page like
// "readme" with no slash is not an area; only paths containing `/`
// contribute. This matches what topLevelDirs() exposes via filesystem
// listing — the two should agree, but areaSummaries is the source of
// truth for the digest because it's driven by indexed content, not
// filesystem state.
func (w *Wiki) areaSummaries(ctx context.Context) ([]AreaSummary, error) {
	rows, err := w.db.QueryContext(ctx, "SELECT path, title FROM pages")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		count      int
		indexTitle string
	}
	bucket := map[string]*acc{}

	for rows.Next() {
		var path, title string
		if err := rows.Scan(&path, &title); err != nil {
			continue
		}
		slash := strings.IndexByte(path, '/')
		if slash < 0 {
			continue // flat-rooted, not an area
		}
		area := path[:slash]
		a, ok := bucket[area]
		if !ok {
			a = &acc{}
			bucket[area] = a
		}
		a.count++
		// The area's index page is `<area>/index`. Record its title
		// once; if for some reason there are multiple (shouldn't be,
		// PRIMARY KEY on path prevents it), the last one wins.
		if path == area+"/index" {
			a.indexTitle = title
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]AreaSummary, 0, len(bucket))
	for name, a := range bucket {
		out = append(out, AreaSummary{
			Path:       name,
			PageCount:  a.count,
			IndexTitle: a.indexTitle,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PageCount != out[j].PageCount {
			return out[i].PageCount > out[j].PageCount
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// renderDigestMarkdown produces the markdown blob shown to LLMs. The
// shape mirrors the example in the plan; ordering of sections is
// header -> cloud line -> Areas -> Recently active -> footer.
//
// When the assembled body exceeds maxBytes the renderer trims:
//  1. drop recents from the tail until under cap, then
//  2. drop cloud terms from the tail until under cap.
//
// Areas are never trimmed — they are the smallest section and the
// most structurally important: an agent that loses the area list
// loses the map of the wiki. The footer hint is also preserved.
//
// If maxBytes <= 0 no trimming is applied. Useful for tests that want
// to verify full content.
func renderDigestMarkdown(d *Digest, maxBytes int) string {
	cloud := d.Cloud
	recents := d.Recents

	for {
		var sb strings.Builder
		writeDigestBody(&sb, d.PageCount, cloud, d.Areas, recents)
		out := sb.String()
		if maxBytes <= 0 || len(out) <= maxBytes {
			return out
		}
		// Trim recents first.
		if len(recents) > 0 {
			recents = recents[:len(recents)-1]
			continue
		}
		// Then trim cloud.
		if len(cloud) > 0 {
			cloud = cloud[:len(cloud)-1]
			continue
		}
		// Already minimal; return what we have, even if over cap.
		// Areas + header alone exceeding 4 KB would require a
		// wiki with hundreds of top-level dirs — unlikely, but
		// truncating areas would be a worse failure mode.
		return out
	}
}

func writeDigestBody(sb *strings.Builder, pageCount int, cloud []CloudTerm, areas []AreaSummary, recents []string) {
	areaCount := len(areas)
	if areaCount == 1 {
		fmt.Fprintf(sb, "This wiki contains %d pages across 1 area.", pageCount)
	} else {
		fmt.Fprintf(sb, "This wiki contains %d pages across %d areas.", pageCount, areaCount)
	}

	if len(cloud) > 0 {
		sb.WriteString(" About:\n")
		for i, t := range cloud {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(t.Term)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("\n")
	}

	if len(areas) > 0 {
		sb.WriteString("\n## Areas\n")
		for _, a := range areas {
			fmt.Fprintf(sb, "- %s (%d)", a.Path, a.PageCount)
			if a.IndexTitle != "" {
				fmt.Fprintf(sb, " — %s/index: %q", a.Path, a.IndexTitle)
			}
			sb.WriteString("\n")
		}
	}

	if len(recents) > 0 {
		sb.WriteString("\n## Recently active\n")
		for _, p := range recents {
			fmt.Fprintf(sb, "- %s\n", p)
		}
	}

	sb.WriteString("\nFull skill: SKILL.md. Use `get_wiki_digest` for the live version.\n")
}
