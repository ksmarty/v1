package memory

import (
	"math"
	"testing"
	"time"
)

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"scaled", []float32{1, 2, 3}, []float32{2, 4, 6}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"length mismatch", []float32{1, 2}, []float32{1}, 0},
		{"empty", nil, nil, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
	}
	for _, c := range cases {
		if got := Cosine(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: Cosine = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTokensDropsStopwordsAndShortTerms(t *testing.T) {
	got := Tokens("The cache IS a map of id to value")
	want := []string{"cache", "map", "of", "id", "to", "value"}
	if len(got) != len(want) {
		t.Fatalf("Tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Tokens = %v, want %v", got, want)
		}
	}
}

func TestKeyword(t *testing.T) {
	// Every meaningful query term appears in the memory.
	if got := Keyword("where is the session list built", "the session list is built in Projects.tsx", ""); got != 1 {
		t.Errorf("full match = %v, want 1", got)
	}
	// Only one of four terms matches.
	got := Keyword("session list built Projects", "the cache lives in store.go", "")
	if got != 0 {
		t.Errorf("no match = %v, want 0", got)
	}
	// An empty query cannot match anything.
	if got := Keyword("", "anything", ""); got != 0 {
		t.Errorf("empty query = %v, want 0", got)
	}
}

func TestKeywordMatchesTags(t *testing.T) {
	// Tags count as content for the lexical term, so a tag hit is found even
	// when the prose does not repeat the word.
	if got := Keyword("sqlite", "we use an embedded database", "sqlite, storage"); got != 1 {
		t.Errorf("tag-only match = %v, want 1", got)
	}
}

func TestTagMatch(t *testing.T) {
	if got := TagMatch("sqlite migration", "sqlite, migrations"); got < 0.9 {
		t.Errorf("both tags = %v, want ~1 (compound 'migrations' should match)", got)
	}
	if got := TagMatch("something unrelated", "sqlite"); got != 0 {
		t.Errorf("no tags = %v, want 0", got)
	}
	if got := TagMatch("sqlite", ""); got != 0 {
		t.Errorf("empty tags = %v, want 0", got)
	}
}

func TestRelevanceFallsBackToLexical(t *testing.T) {
	// With no embedding the content vector must not be consulted at all, and a
	// perfect lexical match should still score high enough to clear the floor.
	got := Relevance(0, 1, 1, false)
	if got < MinRelevance {
		t.Errorf("lexical-only relevance = %v, want >= %v", got, MinRelevance)
	}
	// With an embedding, the weights are the documented blend.
	want := weightContent*0.8 + weightTags*0.5 + weightKeyword*0.4
	if got := Relevance(0.8, 0.5, 0.4, true); math.Abs(got-want) > 1e-9 {
		t.Errorf("embedded relevance = %v, want %v", got, want)
	}
}

func TestRecencyHalves(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	half := now.Add(-RecencyHalfLifeDays * 24 * time.Hour).Unix()
	if got := Recency(half, now); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("one half-life = %v, want 0.5", got)
	}
	if got := Recency(now.Unix(), now); got != 1 {
		t.Errorf("now = %v, want 1", got)
	}
	// A future timestamp (clock skew) must not exceed 1.
	if got := Recency(now.Add(time.Hour).Unix(), now); got != 1 {
		t.Errorf("future = %v, want 1", got)
	}
	if got := Recency(0, now); got != 0 {
		t.Errorf("never accessed = %v, want 0", got)
	}
}

func TestRankRelevanceBeatsImportance(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cands := []Candidate{
		// More important, but about nothing in the query.
		{ID: 1, Content: "the deploy pipeline uses fly.io", Importance: 1.8},
		// Ordinary importance, exactly on topic.
		{ID: 2, Content: "the session list is built in Projects.tsx", Importance: 1},
	}
	hits := Rank(cands, Query{Text: "where is the session list built", Now: now}, 0, MinRelevance)
	if len(hits) == 0 {
		t.Fatal("no hits; the on-topic memory should have cleared the floor")
	}
	if hits[0].ID != 2 {
		t.Fatalf("top hit = %d, want 2 (relevance must dominate importance)", hits[0].ID)
	}
	// The off-topic memory must be dropped, not ranked second.
	for _, h := range hits {
		if h.ID == 1 {
			t.Fatalf("off-topic memory was injected with relevance %v", h.Relevance)
		}
	}
}

// A pinned memory is an explicit statement that it matters in every turn, so it
// is exempt from the relevance floor and leads the section — the limit must not
// be able to squeeze it out.
func TestRankPinnedLeadsAndSurvivesTheFloor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cands := []Candidate{
		{ID: 1, Content: "the deploy pipeline uses fly.io", Importance: PinnedThreshold},
		{ID: 2, Content: "the session list is built in Projects.tsx", Importance: 1},
		{ID: 3, Content: "unrelated trivia", Importance: 1},
	}
	hits := Rank(cands, Query{Text: "where is the session list built", Now: now}, 2, MinRelevance)
	if len(hits) == 0 || hits[0].ID != 1 {
		t.Fatalf("pinned memory did not lead: %+v", hits)
	}
	// The limit of 2 leaves room for the relevant memory as well; the second
	// off-topic, unpinned one is dropped.
	if len(hits) != 2 || hits[1].ID != 2 {
		t.Fatalf("hits = %+v, want the pinned memory then the relevant one", hits)
	}
}

func TestRankEmbeddingOutranksLexical(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cands := []Candidate{
		// Lexically overlapping but semantically unrelated.
		{ID: 1, Content: "session list of build steps in the ci config", Vector: []float32{0, 1}},
		// No lexical overlap, but the embedding says it is the right memory.
		{ID: 2, Content: "chat threads render from a store query", Vector: []float32{1, 0}},
	}
	q := Query{Text: "session list build", Vector: []float32{1, 0}, Now: now}
	hits := Rank(cands, q, 0, MinRelevance)
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].ID != 2 {
		t.Fatalf("top hit = %d, want 2 (the embedding match)", hits[0].ID)
	}
}

func TestRankLimitAndDeterminism(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// Identical content and importance: only the tiebreak can order them.
	cands := []Candidate{
		{ID: 7, Content: "cache key format", Importance: 1},
		{ID: 3, Content: "cache key format", Importance: 1},
		{ID: 5, Content: "cache key format", Importance: 1},
	}
	q := Query{Text: "cache key format", Now: now}
	first := Rank(cands, q, 2, 0)
	if len(first) != 2 {
		t.Fatalf("limit ignored: got %d hits", len(first))
	}
	if first[0].ID != 3 || first[1].ID != 5 {
		t.Fatalf("tiebreak order = %d,%d; want 3,5 (lowest id first)", first[0].ID, first[1].ID)
	}
	second := Rank(cands, q, 2, 0)
	if second[0].ID != first[0].ID || second[1].ID != first[1].ID {
		t.Fatal("ranking is not deterministic across calls")
	}
}

func TestRankWithoutEmbeddingsStillWorks(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cands := []Candidate{
		{ID: 1, Content: "handlers_chat.go writes the SSE frames", Importance: 1},
		{ID: 2, Content: "the deploy target is fly.io", Importance: 1},
	}
	// No vector anywhere: the lexical path is the whole ranking.
	hits := Rank(cands, Query{Text: "SSE frames handlers_chat.go", Now: now}, 0, MinRelevance)
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("lexical-only rank = %+v, want just memory 1", hits)
	}
}
func TestNearest(t *testing.T) {
	vecs := [][]float32{{1, 0}, nil, {0, 1}}
	idx, sim, ok := Nearest(vecs, []float32{0.9, 0.1})
	if !ok || idx != 0 || sim < 0.9 {
		t.Fatalf("Nearest = %d, %v, %v; want index 0 with high similarity", idx, sim, ok)
	}
	// An empty query vector has nothing to compare.
	if _, _, ok := Nearest(vecs, nil); ok {
		t.Fatal("Nearest with an empty vector reported ok")
	}
	// No comparable candidates.
	if _, _, ok := Nearest([][]float32{nil, nil}, []float32{1, 0}); ok {
		t.Fatal("Nearest with no embedded candidates reported ok")
	}
}

func TestDedupThresholdIsMeaningful(t *testing.T) {
	// A near-identical restatement must land above the threshold; a merely
	// related memory must not, or distinct facts would be refused.
	a := []float32{0.9, 0.1, 0.0}
	if Cosine(a, []float32{0.89, 0.11, 0.0}) < DedupThreshold {
		t.Error("a restatement scored below DedupThreshold; duplicates would be stored")
	}
	if Cosine(a, []float32{0.1, 0.9, 0.0}) > DedupThreshold {
		t.Error("an unrelated memory scored above DedupThreshold; distinct facts would be refused")
	}
}
