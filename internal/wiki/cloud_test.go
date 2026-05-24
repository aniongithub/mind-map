package wiki

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestTokenize_Basic(t *testing.T) {
	b := newCloudBuilder(nil)
	got := b.tokenize("Hello, world! This is mind-map.")
	want := []string{"hello", "world", "this", "is", "mind-map"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize: got %v, want %v", got, want)
	}
}

func TestTokenize_KeepsHyphensAndUnderscores(t *testing.T) {
	b := newCloudBuilder(nil)
	got := b.tokenize("page_count and mind-map are tokens")
	if !contains(got, "page_count") {
		t.Fatalf("expected page_count intact: %v", got)
	}
	if !contains(got, "mind-map") {
		t.Fatalf("expected mind-map intact: %v", got)
	}
}

func TestTokenize_StripsWikilinkBrackets(t *testing.T) {
	b := newCloudBuilder(nil)
	got := b.tokenize("see [[projects/mind-map]] for details")
	// '/' is a separator, so we get the segments individually.
	if !contains(got, "projects") || !contains(got, "mind-map") {
		t.Fatalf("wikilink target words missing: %v", got)
	}
	for _, tok := range got {
		if strings.ContainsAny(tok, "[]") {
			t.Fatalf("bracket leaked into token %q", tok)
		}
	}
}

func TestTokenize_LowercasesUnicode(t *testing.T) {
	b := newCloudBuilder(nil)
	got := b.tokenize("Привет МИР")
	want := []string{"привет", "мир"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize unicode: got %v, want %v", got, want)
	}
}

func TestIsStopword(t *testing.T) {
	b := newCloudBuilder([]string{"TODO"})
	cases := map[string]bool{
		"the":   true,  // default
		"todo":  true,  // user-added, case-folded
		"wiki":  false, // domain term, not filtered
		"a":     true,  // length<2 short-circuit (and in defaults)
		"x":     true,  // length<2
		"42":    true,  // all-digit
		"v1":    false, // alnum mix, keep
		"mind":  false,
	}
	for tok, want := range cases {
		if got := b.isStopword(tok); got != want {
			t.Errorf("isStopword(%q) = %v, want %v", tok, got, want)
		}
	}
}

func TestAddPage_UnigramAndBigramCounts(t *testing.T) {
	b := newCloudBuilder(nil)
	uni := map[string]int{}
	bi := map[string]int{}
	b.addPage("wiki engine. wiki engine.", uni, bi)

	if uni["wiki"] != 2 || uni["engine"] != 2 {
		t.Fatalf("unigram counts wrong: %v", uni)
	}
	if bi["wiki engine"] != 2 {
		t.Fatalf("bigram count wrong: %v", bi)
	}
	// "engine wiki" crosses a sentence boundary but our tokenizer
	// treats '.' as a separator, not a sentence-aware split. The
	// resulting bigram across "." is intentional — we don't have
	// sentence info and a bigram across punctuation is still a
	// real adjacent-token pair in the text.
	if bi["engine wiki"] != 1 {
		t.Fatalf("expected one engine->wiki bigram: %v", bi)
	}
}

func TestAddPage_StopwordsFilterBothBigramEnds(t *testing.T) {
	b := newCloudBuilder(nil)
	uni := map[string]int{}
	bi := map[string]int{}
	// "the wiki" → unigram "wiki" counts (the is stopword),
	// but bigram "the wiki" must NOT be recorded.
	b.addPage("the wiki is here. the wiki is here.", uni, bi)

	if _, ok := bi["the wiki"]; ok {
		t.Fatalf("stopword-led bigram leaked: %v", bi)
	}
	if _, ok := bi["wiki is"]; ok {
		t.Fatalf("stopword-tailed bigram leaked: %v", bi)
	}
	if uni["wiki"] != 2 {
		t.Fatalf("unigram counts off: %v", uni)
	}
}

func TestTopK_OrderingAndTieBreak(t *testing.T) {
	counts := map[string]int{
		"banana": 5,
		"apple":  5,
		"cherry": 3,
		"date":   1,
	}
	got := topK(counts, 3)
	want := []CloudTerm{
		{Term: "apple", Count: 5},
		{Term: "banana", Count: 5},
		{Term: "cherry", Count: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("topK: got %v, want %v", got, want)
	}
}

func TestTopK_Empty(t *testing.T) {
	if got := topK(nil, 5); got != nil {
		t.Fatalf("nil input should return nil, got %v", got)
	}
	if got := topK(map[string]int{"a": 1}, 0); got != nil {
		t.Fatalf("k=0 should return nil, got %v", got)
	}
}

func TestBuildCloud_EndToEnd(t *testing.T) {
	w, _ := testWiki(t)
	defer w.Close()
	ctx := context.Background()

	// Seed extra content that should dominate the cloud.
	if err := w.CreatePage(ctx, "topics/sqlite",
		"# SQLite\n\nSQLite is a database. SQLite is fast. SQLite is small.\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	terms, err := w.buildCloud(ctx, 10, nil)
	if err != nil {
		t.Fatalf("buildCloud: %v", err)
	}
	if len(terms) == 0 {
		t.Fatalf("expected non-empty cloud")
	}

	// "sqlite" should be the top unigram now (4+ occurrences across pages).
	found := false
	for _, term := range terms {
		if term.Term == "sqlite" {
			found = true
			if term.Count < 3 {
				t.Errorf("sqlite count surprisingly low: %d", term.Count)
			}
		}
	}
	if !found {
		t.Fatalf("sqlite missing from top-10: %v", terms)
	}

	// No stopwords leaked.
	for _, term := range terms {
		for _, sw := range []string{"the", "is", "a", "and"} {
			if term.Term == sw {
				t.Errorf("stopword %q in cloud", sw)
			}
		}
	}
}

func TestCloudCache_RoundTrip(t *testing.T) {
	c := &cloudCache{}
	if got, ok := c.Get(); ok {
		t.Fatalf("uninitialized cache should report not-set; got %v", got)
	}
	c.Set([]CloudTerm{{Term: "x", Count: 1}})
	got, ok := c.Get()
	if !ok {
		t.Fatalf("after Set, Get should report set")
	}
	if !reflect.DeepEqual(got, []CloudTerm{{Term: "x", Count: 1}}) {
		t.Fatalf("roundtrip mismatch: %v", got)
	}

	// Mutating the returned slice must not affect the cache.
	got[0].Term = "MUTATED"
	again, _ := c.Get()
	if again[0].Term != "x" {
		t.Fatalf("cache leaked internal state: %v", again)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
