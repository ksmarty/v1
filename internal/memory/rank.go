// Package memory ranks a project's stored memories against the conversation.
//
// It is deliberately free of storage and network concerns: the caller supplies
// the candidates and, when embeddings are configured, their vectors, and gets
// back scored hits. Everything here is pure, so ranking can be tested without a
// database or an embedding provider.
//
// The scoring follows the shape used by opencode-mem (a content vector, a
// separate vector for the tags, and a lexical term blended together), with one
// deliberate difference: v1 has carried importance and access decay since before
// this change, so those stay in as a small additive term. They nudge a ranking
// without being able to override relevance — the failure this replaces was
// injecting every memory regardless of whether it had anything to do with the
// turn.
package memory

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Candidate is one stored memory as the ranker sees it.
type Candidate struct {
	ID          int64
	Content     string
	Tags        string
	Category    string
	Importance  float64 // effective importance, already decayed by the store
	LastAccess  int64   // unix seconds, 0 if never retrieved
	AccessCount int
	Vector      []float32 // nil when the memory has no embedding yet
}

// Query is what candidates are scored against.
type Query struct {
	Text   string
	Vector []float32 // nil when no embedding provider is configured
	Now    time.Time
}

// Hit is a scored candidate.
type Hit struct {
	Candidate
	Similarity float64 // cosine against the query vector; 0 when not comparable
	Relevance  float64 // the blended retrieval score, before importance/recency
	Score      float64 // the final ordering key
}

// Weights for the final score. Relevance dominates: importance and recency can
// reorder memories that are equally on-topic, but cannot lift an off-topic one
// above a relevant one.
const (
	weightRelevance  = 0.80
	weightImportance = 0.12
	weightRecency    = 0.08
)

// Weights inside the relevance blend, matching opencode-mem: the content vector
// leads, the tag vector is a strong second signal, and the lexical term catches
// exact identifiers that embeddings tend to blur (file names, symbol names).
const (
	weightContent = 0.5
	weightTags    = 0.3
	weightKeyword = 0.2
)

// MinRelevance is the floor a memory must clear to be injected at all. Below it
// the memory is simply not about this turn, and injecting it costs context and
// distracts the model. opencode-mem uses 0.6 on its own similarity scale; the
// scale here differs (it is a blend, not a raw cosine), so the floor is lower.
const MinRelevance = 0.25

// DedupThreshold is the cosine above which a new memory is considered the same
// fact as an existing one.
//
// opencode-mem only reports near-duplicates for a human to resolve and never
// merges at write time, which is why the same fact accumulates there. v1 stores
// far fewer memories per project (a single agent writes them, and they are
// injected into every prompt), so duplicates are worth refusing at the door.
const DedupThreshold = 0.90

// RecencyHalfLifeDays is how long it takes a memory's recency term to halve.
const RecencyHalfLifeDays = 30

// Cosine returns the cosine similarity of two vectors in [-1, 1]. A zero
// vector, a length mismatch, or an empty input returns 0: callers treat 0 as
// "not comparable", which is also the correct answer for a memory that has no
// embedding yet.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// stopwords are dropped from queries so a question's grammar does not drown the
// terms that carry its meaning.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "these": true, "those": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "whom": true, "whose": true,
	"how": true, "why": true, "does": true, "did": true, "done": true,
	"was": true, "were": true, "are": true, "is": true, "be": true,
	"been": true, "being": true, "am": true, "you": true, "your": true,
	"yours": true, "can": true, "should": true, "would": true, "could": true,
	"about": true, "into": true, "from": true, "have": true, "has": true,
	"had": true, "not": true, "but": true, "nor": true, "yet": true,
	"its": true, "it": true, "at": true, "in": true, "on": true,
	"or": true, "an": true, "as": true, "by": true, "if": true,
	"so": true, "we": true, "they": true, "them": true, "their": true,
	"there": true, "here": true, "then": true, "than": true, "out": true,
	"up": true, "down": true, "over": true, "more": true, "most": true,
	"some": true, "any": true, "all": true, "no": true, "only": true,
	"same": true, "too": true, "very": true, "just": true, "also": true,
	"may": true, "might": true, "must": true, "will": true, "shall": true,
	"do": true, "i": true, "me": true, "my": true, "our": true,
	"ours": true, "he": true, "she": true, "him": true, "her": true,
	"his": true, "use": true, "using": true,
}

// Tokens splits text into comparable lowercase terms.
func Tokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// Single characters match almost anything and carry almost nothing.
		if len(f) < 2 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Keyword scores how much of the query's meaning appears literally in the
// memory. It is the whole signal when no embedding provider is configured, and
// a useful correction when one is: embeddings blur exact identifiers such as
// `handlers_chat.go` or `listSessions`, which are exactly the things worth
// recalling.
// keywordDenomCap bounds the denominator of the lexical match score.
//
// A long question should not dilute a match by adding words: "2 of 6 terms" and
// "2 of 30 terms" are about equally good evidence that the memory is on topic,
// and scoring the second as 0.07 would put it under the relevance floor purely
// because the user typed more. Matching two distinctive terms is the bar.
const keywordDenomCap = 5

func Keyword(query, content, tags string) float64 {
	q := Tokens(query)
	if len(q) == 0 {
		return 0
	}
	hay := strings.ToLower(content + " " + tags)
	var hits float64
	for _, t := range q {
		if strings.Contains(hay, t) {
			hits++
		}
	}
	denom := len(q)
	if denom > keywordDenomCap {
		denom = keywordDenomCap
	}
	if hits > float64(denom) {
		return 1
	}
	return hits / float64(denom)
}

// TagMatch is the fraction of query terms that appear as a tag. A term that is a
// prefix of a tag (or vice versa) counts fully — that is the same word inflected,
// as `migration` is to `migrations` — while a mere substring scores half. Tags
// are short and deliberate, so a hit is a stronger signal than the same word
// appearing in prose.
func TagMatch(query, tags string) float64 {
	q := Tokens(query)
	if len(q) == 0 || strings.TrimSpace(tags) == "" {
		return 0
	}
	have := make(map[string]bool)
	for _, t := range Tokens(tags) {
		have[t] = true
	}
	var hits float64
	for _, t := range q {
		if have[t] {
			hits++
			continue
		}
		for h := range have {
			if strings.HasPrefix(h, t) || strings.HasPrefix(t, h) {
				hits++
				break
			}
			if strings.Contains(h, t) || strings.Contains(t, h) {
				hits += 0.5
				break
			}
		}
	}
	if hits > float64(len(q)) {
		hits = float64(len(q))
	}
	return hits / float64(len(q))
}

// Relevance blends the available signals. When the query has no vector (no
// embedding provider, or an embedding that failed), it falls back to the
// lexical terms alone rather than returning nothing.
func Relevance(contentSim, tagSim, keyword float64, embedded bool) float64 {
	if !embedded {
		// Lexical-only: tags are still a real signal, content matching is the
		// rest. Scale so a full lexical match scores near 1.
		return 0.7*keyword + 0.3*tagSim
	}
	return weightContent*contentSim + weightTags*tagSim + weightKeyword*keyword
}

// Recency returns a 1..0 term that halves every RecencyHalfLifeDays. A memory
// never retrieved falls back to its age, which the caller passes as created.
func Recency(unix int64, now time.Time) float64 {
	if unix <= 0 {
		return 0
	}
	ageDays := now.Sub(time.Unix(unix, 0)).Hours() / 24
	if ageDays <= 0 {
		return 1
	}
	return math.Pow(0.5, ageDays/RecencyHalfLifeDays)
}

// Importance normalizes the store's 0..3 scale to 0..1.
func Importance(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 3 {
		return 1
	}
	return v / 3
}

// PinnedThreshold is the importance at or above which a memory is never
// dropped and never crowded out by the limit.
//
// v1 promises that importance 2+ "pins the memory so it never decays", and a
// pinned fact is by definition one that matters in every turn whatever the user
// happens to be asking about — so the relevance floor, which exists to keep
// off-topic noise out, does not apply to it.
const PinnedThreshold = 2

// Rank scores every candidate and returns the best ones, highest first.
//
// Candidates that clear no signal at all are dropped rather than filling the
// limit: injecting a memory that has nothing to do with the turn is worse than
// injecting fewer. Pinned candidates are exempt from that floor. limit <= 0
// means "no limit", which the caller should only use where the whole set is
// genuinely wanted (the memories page).
func Rank(cands []Candidate, q Query, limit int, floor float64) []Hit {
	embedded := len(q.Vector) > 0
	hits := make([]Hit, 0, len(cands))
	for _, c := range cands {
		var sim float64
		if embedded && len(c.Vector) > 0 {
			sim = Cosine(c.Vector, q.Vector)
		}
		tagSim := TagMatch(q.Text, c.Tags)
		kw := Keyword(q.Text, c.Content, c.Tags)
		rel := Relevance(sim, tagSim, kw, embedded && len(c.Vector) > 0)
		last := c.LastAccess
		if last == 0 {
			// Never retrieved: age is the honest signal.
			last = 0
		}
		rec := Recency(last, q.Now)
		score := weightRelevance*rel +
			weightImportance*Importance(c.Importance) +
			weightRecency*rec
		hits = append(hits, Hit{Candidate: c, Similarity: sim, Relevance: rel, Score: score})
	}
	// A memory that matched nothing lexically and has no usable vector is noise —
	// unless it is pinned, which is an explicit statement that it matters anyway.
	kept := hits[:0]
	for _, h := range hits {
		if h.Relevance >= floor || h.Importance >= PinnedThreshold {
			kept = append(kept, h)
		}
	}
	hits = kept
	sort.SliceStable(hits, func(i, j int) bool {
		// Pinned memories lead, so the limit can never squeeze one out.
		pi := hits[i].Importance >= PinnedThreshold
		pj := hits[j].Importance >= PinnedThreshold
		if pi != pj {
			return pi
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		// Deterministic tiebreak so equal scores do not reorder between calls.
		return hits[i].ID < hits[j].ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Nearest returns the closest existing memory to a vector and its similarity,
// used to refuse a duplicate at write time. It returns ok=false when nothing is
// comparable.
func Nearest(vecs [][]float32, v []float32) (idx int, sim float64, ok bool) {
	if len(v) == 0 {
		return 0, 0, false
	}
	best := -1
	for i, other := range vecs {
		if len(other) == 0 {
			continue
		}
		s := Cosine(other, v)
		if best < 0 || s > sim {
			best, sim = i, s
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return best, sim, true
}
