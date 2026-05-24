package wiki

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// CloudTerm is a single entry in the rendered word/phrase cloud.
type CloudTerm struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// defaultStopwords is the built-in English stopword list applied to
// every wiki's cloud. Users add domain-specific extras via config
// (digest.stopwords_extra) which are merged on top.
//
// Kept intentionally conservative: only true function words and the
// most generic English filler. Domain terms (even common ones like
// "wiki" or "page") are left to the per-wiki frequency signal to
// dampen — a wiki that's literally *about* wikis should be allowed
// to say so.
var defaultStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {},
	"be": {}, "but": {}, "by": {}, "can": {},
	"do": {}, "does": {}, "for": {}, "from": {},
	"had": {}, "has": {}, "have": {}, "he": {}, "her": {}, "here": {},
	"hers": {}, "him": {}, "his": {}, "how": {},
	"i": {}, "if": {}, "in": {}, "into": {}, "is": {}, "it": {}, "its": {},
	"just": {}, "may": {}, "might": {}, "must": {},
	"no": {}, "not": {}, "now": {}, "of": {}, "off": {}, "on": {}, "one": {},
	"only": {}, "or": {}, "other": {}, "our": {}, "ours": {}, "out": {},
	"over": {}, "own": {},
	"s": {}, "she": {}, "should": {}, "so": {}, "some": {}, "such": {},
	"t": {}, "than": {}, "that": {}, "the": {}, "their": {}, "them": {},
	"then": {}, "there": {}, "these": {}, "they": {}, "this": {}, "those": {},
	"to": {}, "too": {},
	"under": {}, "until": {}, "up": {}, "upon": {},
	"was": {}, "we": {}, "were": {}, "what": {}, "when": {}, "where": {},
	"which": {}, "while": {}, "who": {}, "whom": {}, "why": {}, "will": {},
	"with": {}, "would": {},
	"you": {}, "your": {}, "yours": {},
}

// cloudBuilder accumulates unigram and bigram counts across pages.
// It is reset and re-run from scratch on each rebuild; the plan's
// 5-minute ticker (Step 6) calls Build() and stores the result.
type cloudBuilder struct {
	stopwords map[string]struct{}
}

// newCloudBuilder constructs a builder with the default stopword set
// merged with the user's extras. Extras are case-folded to match the
// tokenizer's lowercase output.
func newCloudBuilder(extra []string) *cloudBuilder {
	sw := make(map[string]struct{}, len(defaultStopwords)+len(extra))
	for k := range defaultStopwords {
		sw[k] = struct{}{}
	}
	for _, w := range extra {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			sw[w] = struct{}{}
		}
	}
	return &cloudBuilder{stopwords: sw}
}

// isStopword reports whether t is filtered out of the cloud. In
// addition to the configured stopword set, single-character tokens
// and pure-numeric tokens are dropped: neither carries useful "about"
// signal and both massively inflate the long tail.
func (b *cloudBuilder) isStopword(t string) bool {
	if len(t) < 2 {
		return true
	}
	if _, ok := b.stopwords[t]; ok {
		return true
	}
	allDigit := true
	for _, r := range t {
		if !unicode.IsDigit(r) {
			allDigit = false
			break
		}
	}
	return allDigit
}

// tokenize splits a body into lowercase word tokens. The rules are
// deliberately simple and deterministic:
//
//   - Lowercase everything.
//   - A token is a maximal run of letters / digits / underscores /
//     hyphens. Hyphens and underscores are kept because identifiers
//     like "mind-map" or "page_count" are exactly the kinds of terms
//     we want to surface intact.
//   - Wikilink markup ([[...]]) is stripped but the target text
//     inside is tokenized normally — a link to [[projects/mind-map]]
//     contributes "projects" and "mind-map" to the page's tokens.
//   - Markdown punctuation (#, *, _, `, etc.) becomes a separator.
//   - Code fences and inline code are NOT stripped: code identifiers
//     are part of what a technical wiki is about, and dropping them
//     flattens the cloud.
func (b *cloudBuilder) tokenize(body string) []string {
	// Cheaply strip the wikilink delimiters so [[a/b]] surfaces both
	// "a" and "b" without us having to special-case the parser. The
	// pipe form [[display|target]] is left as-is; the tokenizer's
	// non-alnum-split will handle both halves.
	body = strings.ReplaceAll(body, "[[", " ")
	body = strings.ReplaceAll(body, "]]", " ")

	tokens := make([]string, 0, len(body)/6)
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range body {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(unicode.ToLower(r))
		case r == '-' || r == '_':
			// Mid-token punctuation: keep only if it joins two
			// alnum runs. Leading/trailing get trimmed below.
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()

	// Trim leading/trailing hyphens and underscores (e.g. "--foo")
	// that survived the above without splitting cleanly.
	for i, t := range tokens {
		tokens[i] = strings.Trim(t, "-_")
	}
	return tokens
}

// addPage folds one page's tokens into the running unigram and bigram
// counts.
//
// Bigrams require BOTH ends to pass the stopword filter (plan open
// question #2 lean): otherwise common phrases like "the wiki" would
// dominate purely because "the" is high-frequency, even though the
// pair is no more informative than "wiki" alone.
func (b *cloudBuilder) addPage(body string, unigrams, bigrams map[string]int) {
	tokens := b.tokenize(body)

	var prev string
	for _, t := range tokens {
		if t == "" {
			prev = ""
			continue
		}
		stop := b.isStopword(t)
		if !stop {
			unigrams[t]++
		}
		if prev != "" && !stop && !b.isStopword(prev) {
			bigrams[prev+" "+t]++
		}
		prev = t
	}
}

// topK selects the K highest-count entries from the given map. Ties
// break alphabetically so the output is stable across rebuilds —
// otherwise a digest cache invalidation could shuffle the cloud for
// no reason a user would understand.
func topK(counts map[string]int, k int) []CloudTerm {
	if k <= 0 || len(counts) == 0 {
		return nil
	}
	terms := make([]CloudTerm, 0, len(counts))
	for t, n := range counts {
		terms = append(terms, CloudTerm{Term: t, Count: n})
	}
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].Count != terms[j].Count {
			return terms[i].Count > terms[j].Count
		}
		return terms[i].Term < terms[j].Term
	})
	if len(terms) > k {
		terms = terms[:k]
	}
	return terms
}

// buildCloud computes the top-K most frequent terms across all page
// bodies. The result mixes unigrams and bigrams: bigrams are scored
// by their own frequency (no boost), so a phrase only beats a single
// word when it genuinely occurs more often.
//
// Caller owns the goroutine and the slot it's stored in; this function
// just does the work. Step 6 wires it to the 5-minute ticker.
// BuildCloud computes the top-K most frequent terms across all page
// bodies. Exposed for the digest.Manager ticker — the implementation
// lives on the Wiki because it reads `pages` directly; the supervisor
// owns the scheduling.
//
// The result mixes unigrams and bigrams: bigrams are scored by their
// own frequency (no boost), so a phrase only beats a single word when
// it genuinely occurs more often.
func (w *Wiki) BuildCloud(ctx context.Context, k int, stopwordsExtra []string) ([]CloudTerm, error) {
	return w.buildCloud(ctx, k, stopwordsExtra)
}

// SetCloud installs a freshly-built cloud into the in-memory cache.
// Pairs with BuildCloud; the supervisor calls Build → Set → Persist.
func (w *Wiki) SetCloud(terms []CloudTerm) {
	w.cloud.Set(terms)
}

// PersistCloud writes the current cloud cache to wiki_state. Called
// by the digest.Manager after a successful rebuild.
func (w *Wiki) PersistCloud(ctx context.Context) error {
	return w.persistCloud(ctx)
}

func (w *Wiki) buildCloud(ctx context.Context, k int, stopwordsExtra []string) ([]CloudTerm, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := w.db.QueryContext(ctx, "SELECT body FROM pages")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	b := newCloudBuilder(stopwordsExtra)
	unigrams := make(map[string]int)
	bigrams := make(map[string]int)

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var body string
		if err := rows.Scan(&body); err != nil {
			continue
		}
		b.addPage(body, unigrams, bigrams)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Merge the two count maps before selecting top-K. This lets a
	// strong bigram outrank a weak unigram, and vice versa, on a
	// single global scale.
	merged := make(map[string]int, len(unigrams)+len(bigrams))
	for t, n := range unigrams {
		merged[t] = n
	}
	for t, n := range bigrams {
		merged[t] = n
	}
	return topK(merged, k), nil
}

// cloudCache is a single-slot cache for the rebuilt cloud. The
// 5-minute ticker (Step 6) calls Set; readers (digest renderer) call
// Get. A read returns whatever was last set even if the ticker is
// behind — the digest's job is "good orientation," not "perfectly
// fresh stats."
type cloudCache struct {
	mu    sync.RWMutex
	terms []CloudTerm
	// set is true once Set has been called at least once. Readers
	// distinguish "no cloud yet" (cold start) from "cloud is empty"
	// (truly empty wiki) by checking set.
	set bool
	// version is bumped on each Set. The digest cache uses it as a
	// change signal so it can invalidate rendered output without
	// re-comparing slices.
	version uint64
}

func (c *cloudCache) Set(terms []CloudTerm) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Defensive copy: caller may continue to mutate the slice.
	cp := make([]CloudTerm, len(terms))
	copy(cp, terms)
	c.terms = cp
	c.set = true
	c.version++
}

// Get returns a copy of the current cloud and whether one has been
// computed yet.
func (c *cloudCache) Get() ([]CloudTerm, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.set {
		return nil, false
	}
	cp := make([]CloudTerm, len(c.terms))
	copy(cp, c.terms)
	return cp, true
}

// Version returns the monotonic change counter. Pairs with
// recentsLRU.version() for digest cache invalidation.
func (c *cloudCache) Version() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}
