package embed

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// golden holds vectors produced by the reference implementation: the same model
// run through onnxruntime with the Hugging Face `tokenizers` library, mean-pooled
// and L2-normalised.
type golden struct {
	Texts   []string    `json:"texts"`
	Vectors [][]float32 `json:"vectors"`
	Dim     int         `json:"dim"`
}

// TestNativeMatchesReference is the check that makes the native provider
// trustworthy: it asserts the Go encoder reproduces the reference vectors.
//
// Without it, a subtly wrong normalisation, a transposed weight or the wrong
// layer-norm epsilon would still produce plausible-looking vectors — they would
// just be wrong, and retrieval would quietly get worse rather than fail.
//
// It is skipped unless V1_EMBED_TEST_MODEL names a downloaded model directory,
// because the weights are ~90MB and are not vendored.
func TestNativeMatchesReference(t *testing.T) {
	dir := os.Getenv("V1_EMBED_TEST_MODEL")
	if dir == "" {
		t.Skip("set V1_EMBED_TEST_MODEL=<model dir> to run the reference parity check")
	}
	goldenPath := os.Getenv("V1_EMBED_TEST_GOLDEN")
	if goldenPath == "" {
		goldenPath = filepath.Join("testdata", "minilm_golden.json")
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	m, err := loadModel(dir, PoolMean)
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	if m.cfg.HiddenSize != g.Dim {
		t.Fatalf("model width %d, golden width %d", m.cfg.HiddenSize, g.Dim)
	}

	// Tolerance is set from measured agreement, not guessed: the two
	// implementations accumulate float32 rounding in a different order across
	// six layers, and a real mistake (a transposed weight, a wrong epsilon)
	// moves a component by orders of magnitude more than this.
	const tol = 1e-5
	worst := 0.0
	worstAt := ""
	for i, text := range g.Texts {
		got, err := m.Embed(text)
		if err != nil {
			t.Fatalf("embed %q: %v", text, err)
		}
		want := g.Vectors[i]
		if len(got) != len(want) {
			t.Fatalf("%q: got %d dims, want %d", text, len(got), len(want))
		}
		for j := range got {
			d := math.Abs(float64(got[j] - want[j]))
			if d > worst {
				worst, worstAt = d, text
			}
			if d > tol {
				t.Errorf("%q: component %d differs by %.6f (got %.6f want %.6f)", text, j, d, got[j], want[j])
			}
		}
		// Cosine is the comparison that actually matters downstream.
		if c := cosine(got, want); c < 0.9999 {
			t.Errorf("%q: cosine with reference is %.6f, want >= 0.9999", text, c)
		}
	}
	t.Logf("max component difference %.8f (at %q), tolerance %.0e", worst, worstAt, tol)
}

func cosine(a, b []float32) float64 {
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
