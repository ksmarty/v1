package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestTokenizerMatchesReference checks token ids against the Hugging Face
// `tokenizers` library for the same model.
//
// It uses a trimmed tokenizer.json fixture — 93 tokens, kept at their original
// ids — rather than the 466KB original, so it runs in CI with no model download.
// Because the ids are the real model's, a normalisation or WordPiece mistake
// still fails here, and unlike the full parity check this one always runs.
func TestTokenizerMatchesReference(t *testing.T) {
	var ref struct {
		Texts []string  `json:"texts"`
		IDs   [][]int32 `json:"ids"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "minilm_token_ids.json"))
	if err != nil {
		t.Fatalf("read golden ids: %v", err)
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatalf("parse golden ids: %v", err)
	}
	tok, err := loadTokenizer(filepath.Join("testdata", "minilm_tokenizer"), 512)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	if tok.cls != 101 || tok.sep != 102 || tok.unk != 100 {
		t.Errorf("special ids: cls=%d sep=%d unk=%d, want 101/102/100", tok.cls, tok.sep, tok.unk)
	}
	for i, text := range ref.Texts {
		got := tok.Encode(text)
		want := ref.IDs[i]
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q\n got  %v\n want %v", text, got, want)
		}
	}
}

// TestTokenizeCoversTheTrickyCases pins the behaviour that is easy to get
// subtly wrong, independent of the golden file.
func TestTokenizeCoversTheTrickyCases(t *testing.T) {
	tok, err := loadTokenizer(filepath.Join("testdata", "minilm_tokenizer"), 512)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	tests := []struct {
		name string
		text string
		// want is the token count excluding [CLS]/[SEP].
		want int
	}{
		{"accents fold to the unaccented token", "café", 1},
		{"uppercase folds to lowercase", "CAFÉ", 1},
		{"each CJK ideograph is its own token", "中文测试", 4},
		{"punctuation is isolated", "it's", 3},
		{"a lone symbol is a token", "$", 1},
		{"whitespace-only yields no tokens", "   ", 0},
		{"empty yields no tokens", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tok.Encode(tc.text)
			if len(got) != tc.want+2 {
				t.Fatalf("%q: got %v (%d tokens), want %d content tokens", tc.text, got, len(got)-2, tc.want)
			}
			if got[0] != tok.cls || got[len(got)-1] != tok.sep {
				t.Fatalf("%q: missing [CLS]/[SEP] wrapper: %v", tc.text, got)
			}
		})
	}
}

func TestEncodeRespectsMaxLength(t *testing.T) {
	dir := filepath.Join("testdata", "minilm_tokenizer")
	tok, err := loadTokenizer(dir, 8)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	ids := tok.Encode("the quick brown fox jumps over the lazy dog and then some more words")
	if len(ids) > 8 {
		t.Fatalf("got %d tokens, max is 8", len(ids))
	}
	if ids[0] != tok.cls || ids[len(ids)-1] != tok.sep {
		t.Fatalf("truncation dropped a special token: %v", ids)
	}
}

func TestParseModelRef(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in       string
		wantRepo string
		wantDir  bool
		wantErr  bool
	}{
		{in: "sentence-transformers/all-MiniLM-L6-v2", wantRepo: "sentence-transformers/all-MiniLM-L6-v2"},
		{in: "hf:sentence-transformers/all-MiniLM-L6-v2", wantRepo: "sentence-transformers/all-MiniLM-L6-v2"},
		{in: "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2", wantRepo: "sentence-transformers/all-MiniLM-L6-v2"},
		{in: "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/tree/main", wantRepo: "sentence-transformers/all-MiniLM-L6-v2"},
		{in: "https://www.huggingface.co/BAAI/bge-small-en-v1.5", wantRepo: "BAAI/bge-small-en-v1.5"},
		{in: "", wantRepo: DefaultNativeModel},
		{in: dir, wantDir: true},
		{in: "not a repo", wantErr: true},
		{in: "https://example.com/model", wantErr: true},
		{in: "justaname", wantErr: true},
	}
	for _, tc := range tests {
		repo, gotDir, err := parseModelRef(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error, got repo=%q dir=%q", tc.in, repo, gotDir)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
			continue
		}
		if tc.wantDir {
			if gotDir != dir {
				t.Errorf("%q: dir=%q, want %q", tc.in, gotDir, dir)
			}
			continue
		}
		if repo != tc.wantRepo {
			t.Errorf("%q: repo=%q, want %q", tc.in, repo, tc.wantRepo)
		}
	}
}

// TestValidateConfigRejectsUnsupported pins the promise that an unsupported
// architecture fails loudly. A model that loaded and produced plausible but
// wrong vectors would be far worse than a refusal.
func TestValidateConfigRejectsUnsupported(t *testing.T) {
	base := bertConfig{
		ModelType: "bert", HiddenSize: 384, NumHiddenLayers: 6,
		NumAttentionHeads: 12, IntermediateSize: 1536, MaxPositionEmbeddings: 512,
		PositionEmbeddingType: "absolute", HiddenAct: "gelu",
	}
	if err := validateConfig(base); err != nil {
		t.Fatalf("baseline config should be valid: %v", err)
	}
	for _, modelType := range []string{"roberta", "mpnet", "deberta-v2", "modernbert", "gemma", "gemma2", "nomic_bert"} {
		cfg := base
		cfg.ModelType = modelType
		if err := validateConfig(cfg); err == nil {
			t.Errorf("model_type %q was accepted; it would produce wrong vectors", modelType)
		}
	}
	bad := base
	bad.HiddenSize = 385 // not divisible by 12 heads
	if err := validateConfig(bad); err == nil {
		t.Error("an indivisible hidden_size was accepted")
	}
	bad = base
	bad.PositionEmbeddingType = "relative_key"
	if err := validateConfig(bad); err == nil {
		t.Error("a relative position embedding was accepted")
	}
	bad = base
	bad.HiddenAct = "silu"
	if err := validateConfig(bad); err == nil {
		t.Error("an unsupported activation was accepted")
	}
}

func TestCheckPreTokenizerRejectsOtherSplitters(t *testing.T) {
	ok := []string{
		`{"type":"BertPreTokenizer"}`,
		`{"type":"Sequence","pretokenizers":[{"type":"BertPreTokenizer"}]}`,
		``,
	}
	for _, raw := range ok {
		if err := checkPreTokenizer(json.RawMessage(raw)); err != nil {
			t.Errorf("%s: unexpected error: %v", raw, err)
		}
	}
	bad := []string{
		`{"type":"ByteLevel"}`,
		`{"type":"Metaspace"}`,
		`{"type":"Sequence","pretokenizers":[{"type":"BertPreTokenizer"},{"type":"Digits","individual_digits":true}]}`,
	}
	for _, raw := range bad {
		if err := checkPreTokenizer(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: was accepted; it would tokenize differently", raw)
		}
	}
}
