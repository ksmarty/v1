// wordpiece.go implements the BERT WordPiece tokenizer.
//
// It follows the pipeline Hugging Face records in tokenizer.json: normalise
// (clean, split CJK, lowercase, strip accents), pre-tokenize (whitespace, then
// isolate punctuation), then greedy longest-match WordPiece with a "##"
// continuation prefix. Matching the reference pipeline exactly is what keeps a
// native embedding identical to the same model run through transformers.
package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// tokenizer is a WordPiece tokenizer plus the settings that decide how text is
// normalised before it is matched against the vocabulary.
type tokenizer struct {
	vocab map[string]int32

	unk    int32
	cls    int32
	sep    int32
	prefix string // continuation marker, "##" for BERT

	lowercase    bool
	stripAccents bool
	cleanText    bool
	handleCJK    bool

	maxChars int // longest word to attempt, in characters
	maxLen   int // tokens including the specials
}

type tokenizerFile struct {
	Model struct {
		Type                    string           `json:"type"`
		Vocab                   map[string]int32 `json:"vocab"`
		UnkToken                string           `json:"unk_token"`
		ContinuingSubwordPrefix string           `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int              `json:"max_input_chars_per_word"`
	} `json:"model"`
	Normalizer struct {
		Type          string `json:"type"`
		CleanText     *bool  `json:"clean_text"`
		HandleChinese *bool  `json:"handle_chinese_chars"`
		Lowercase     *bool  `json:"lowercase"`
		StripAccents  *bool  `json:"strip_accents"`
	} `json:"normalizer"`
	PostProcessor struct {
		Type   string `json:"type"`
		Single []struct {
			SpecialToken *struct {
				ID string `json:"id"`
			} `json:"SpecialToken"`
		} `json:"single"`
	} `json:"post_processor"`
	PreTokenizer json.RawMessage `json:"pre_tokenizer"`
}

// loadTokenizer reads tokenizer.json, falling back to vocab.txt.
//
// vocab.txt is the older BERT export and carries no normalisation settings, so
// the BERT defaults are used: lowercase, strip accents, clean text, split CJK.
func loadTokenizer(dir string, maxLen int) (*tokenizer, error) {
	path := dir + "/tokenizer.json"
	b, err := os.ReadFile(path)
	if err != nil {
		return loadVocabTxt(dir, maxLen)
	}
	var f tokenizerFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tokenizer.json: %w", err)
	}
	if f.Model.Type != "" && f.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("tokenizer.json: model type %q is not WordPiece", f.Model.Type)
	}
	if len(f.Model.Vocab) == 0 {
		return loadVocabTxt(dir, maxLen)
	}

	t := &tokenizer{
		vocab:        f.Model.Vocab,
		prefix:       f.Model.ContinuingSubwordPrefix,
		maxChars:     f.Model.MaxInputCharsPerWord,
		maxLen:       maxLen,
		lowercase:    boolOr(f.Normalizer.Lowercase, true),
		cleanText:    boolOr(f.Normalizer.CleanText, true),
		handleCJK:    boolOr(f.Normalizer.HandleChinese, true),
		stripAccents: boolOr(f.Normalizer.StripAccents, boolOr(f.Normalizer.Lowercase, true)),
	}
	if t.prefix == "" {
		t.prefix = "##"
	}
	if t.maxChars == 0 {
		t.maxChars = 100
	}
	// The normalizer may be absent (a raw tokenizer) — then no lowercasing is
	// recorded and applying it anyway would corrupt a cased vocabulary.
	if f.Normalizer.Type == "" {
		t.lowercase, t.stripAccents, t.cleanText, t.handleCJK = false, false, false, false
	}
	if f.Normalizer.Type != "" && f.Normalizer.Type != "BertNormalizer" && f.Normalizer.Type != "Sequence" {
		return nil, fmt.Errorf("tokenizer.json: normalizer %q is not supported (expected BertNormalizer)", f.Normalizer.Type)
	}
	if err := checkPreTokenizer(f.PreTokenizer); err != nil {
		return nil, err
	}

	t.unk = t.lookup(f.Model.UnkToken, "[UNK]")
	t.cls, t.sep = t.specials(f)
	return t, nil
}

// checkPreTokenizer rejects a tokenizer that splits text differently from the
// BertPreTokenizer this package implements. Accepting one silently would change
// every token id and therefore every embedding, so an unknown splitter is a load
// error rather than a wrong vector.
func checkPreTokenizer(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("tokenizer.json: pre_tokenizer: %w", err)
	}
	types := map[string]bool{}
	collectTypes(v, types)
	for name := range types {
		// Sequence is a wrapper, not a splitter: a Sequence containing only
		// BertPreTokenizer splits exactly the way this package does.
		if name == "Sequence" {
			continue
		}
		if name != "BertPreTokenizer" {
			return fmt.Errorf("tokenizer.json: pre_tokenizer %q is not supported (only BertPreTokenizer)", name)
		}
	}
	return nil
}

// collectTypes gathers every "type" in a nested pre_tokenizer definition, so a
// Sequence wrapping BertPreTokenizer is accepted while ByteLevel, Metaspace and
// the other splitters are not.
func collectTypes(v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["type"].(string); ok {
			out[s] = true
		}
		for k, sub := range t {
			if k == "type" {
				continue
			}
			collectTypes(sub, out)
		}
	case []any:
		for _, sub := range t {
			collectTypes(sub, out)
		}
	}
}

// loadVocabTxt reads a plain one-token-per-line vocabulary.
func loadVocabTxt(dir string, maxLen int) (*tokenizer, error) {
	b, err := os.ReadFile(dir + "/vocab.txt")
	if err != nil {
		return nil, fmt.Errorf("no tokenizer.json or vocab.txt in %s", dir)
	}
	vocab := make(map[string]int32, 4096)
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if _, ok := vocab[line]; !ok {
			vocab[line] = int32(i)
		}
	}
	t := &tokenizer{
		vocab: vocab, prefix: "##", maxChars: 100, maxLen: maxLen,
		lowercase: true, stripAccents: true, cleanText: true, handleCJK: true,
	}
	t.unk = t.lookup("", "[UNK]")
	t.cls, t.sep = t.lookup("", "[CLS]"), t.lookup("", "[SEP]")
	return t, nil
}

// lookup returns the id for the first token that is present.
func (t *tokenizer) lookup(tokens ...string) int32 {
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if id, ok := t.vocab[tok]; ok {
			return id
		}
	}
	return 0
}

// specials reads the [CLS]/[SEP] ids the post-processor names, defaulting to the
// BERT pair when it does not say.
func (t *tokenizer) specials(f tokenizerFile) (int32, int32) {
	var cls, sep int32 = -1, -1
	for _, s := range f.PostProcessor.Single {
		if s.SpecialToken == nil {
			continue
		}
		switch s.SpecialToken.ID {
		case "[CLS]":
			cls = t.lookup("[CLS]")
		case "[SEP]":
			sep = t.lookup("[SEP]")
		}
	}
	if cls < 0 {
		cls = t.lookup("[CLS]")
	}
	if sep < 0 {
		sep = t.lookup("[SEP]")
	}
	return cls, sep
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Encode returns the token ids for text, truncated to the model's limit.
//
// Truncation drops from the end rather than the middle: for a sentence
// embedding the opening is the part that carries the topic, and the reference
// pipeline truncates the same way.
func (t *tokenizer) Encode(text string) []int32 {
	text = t.normalize(text)
	limit := t.maxLen - 2 // room for [CLS] and [SEP]
	if limit < 1 {
		limit = 1
	}
	ids := make([]int32, 0, limit+2)
	ids = append(ids, t.cls)
	for _, word := range preTokenize(text) {
		for _, id := range t.wordpiece(word) {
			if len(ids)-1 >= limit {
				break
			}
			ids = append(ids, id)
		}
		if len(ids)-1 >= limit {
			break
		}
	}
	ids = append(ids, t.sep)
	return ids
}

// normalize applies the BertNormalizer stages in the reference order: clean,
// split CJK, lowercase, then strip accents.
func (t *tokenizer) normalize(s string) string {
	if t.cleanText {
		s = cleanText(s)
	}
	if t.handleCJK {
		s = splitCJK(s)
	}
	if t.lowercase {
		s = strings.ToLower(s)
	}
	if t.stripAccents {
		s = stripAccents(s)
	}
	return s
}

// cleanText drops NUL, the replacement character and control characters, and
// maps every other whitespace rune to a plain space.
func cleanText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0 || r == 0xfffd || isControl(r):
			continue
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isControl reports whether r is a C0/C1 control or a format character, which is
// what the reference's `is_control` covers.
func isControl(r rune) bool {
	switch {
	case r >= 0x00 && r <= 0x1f, r >= 0x7f && r <= 0x9f:
		return true
	case r == 0xad: // soft hyphen
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x206f:
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// splitCJK surrounds each CJK ideograph with spaces so the pre-tokenizer emits
// them one per token, which is what BERT does for Chinese and Japanese.
func splitCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4e00 && r <= 0x9fff,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x20000 && r <= 0x2a6df,
		r >= 0x2a700 && r <= 0x2b73f,
		r >= 0x2b740 && r <= 0x2b81f,
		r >= 0x2b820 && r <= 0x2ceaf,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0x2f800 && r <= 0x2fa1f:
		return true
	}
	return false
}

// stripAccents decomposes to NFD and drops combining marks, so "café" matches
// the vocabulary's "cafe".
func stripAccents(s string) string {
	if isASCII(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// preTokenize splits on whitespace and then isolates punctuation, matching
// BertPreTokenizer: every punctuation character becomes its own word.
func preTokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			flush()
		case isPunct(r):
			flush()
			out = append(out, string(r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// isPunct mirrors the reference `_is_punctuation`: the ASCII symbol ranges plus
// any Unicode punctuation category. The explicit ASCII ranges matter because
// characters like "$" are Unicode symbols, not punctuation, yet BERT splits them.
func isPunct(r rune) bool {
	switch {
	case r >= 33 && r <= 47, r >= 58 && r <= 64, r >= 91 && r <= 96, r >= 123 && r <= 126:
		return true
	}
	return unicode.IsPunct(r)
}

// wordpiece splits one pre-token into subword ids by greedy longest match,
// prefixing continuations with "##". A word with no full decomposition becomes
// [UNK] rather than a partial match, which is what the reference does.
func (t *tokenizer) wordpiece(word string) []int32 {
	chars := []rune(word)
	if len(chars) == 0 {
		return nil
	}
	if len(chars) > t.maxChars {
		return []int32{t.unk}
	}
	var ids []int32
	for start := 0; start < len(chars); {
		end := len(chars)
		var matched string
		var matchedEnd int
		for end > start {
			piece := string(chars[start:end])
			if start > 0 {
				piece = t.prefix + piece
			}
			if _, ok := t.vocab[piece]; ok {
				matched, matchedEnd = piece, end
				break
			}
			end--
		}
		if matched == "" {
			return []int32{t.unk}
		}
		ids = append(ids, t.vocab[matched])
		start = matchedEnd
	}
	return ids
}
