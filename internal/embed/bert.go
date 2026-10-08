// bert.go runs a BERT encoder forward pass in pure Go.
//
// This is the part that makes native embeddings possible without cgo: the
// transformer encoder is a small, fixed sequence of matrix multiplies,
// layer norms and a softmax, so it can be implemented directly rather than
// pulled in through an ONNX runtime. Only the architectures that fit this shape
// are accepted, and loadModel rejects the rest by name instead of producing
// quietly wrong vectors.
package embed

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// PoolingMode selects how token vectors are reduced to one sentence vector.
type PoolingMode string

const (
	PoolMean PoolingMode = "mean" // mask-weighted mean of token vectors
	PoolCLS  PoolingMode = "cls"  // the [CLS] token's vector
)

type bertConfig struct {
	ModelType             string  `json:"model_type"`
	HiddenSize            int     `json:"hidden_size"`
	NumHiddenLayers       int     `json:"num_hidden_layers"`
	NumAttentionHeads     int     `json:"num_attention_heads"`
	IntermediateSize      int     `json:"intermediate_size"`
	MaxPositionEmbeddings int     `json:"max_position_embeddings"`
	LayerNormEps          float64 `json:"layer_norm_eps"`
	HiddenAct             string  `json:"hidden_act"`
	VocabSize             int     `json:"vocab_size"`
	TypeVocabSize         int     `json:"type_vocab_size"`
	// PositionEmbeddingType must be absolute. RoBERTa (learned offsets) and the
	// rotary/relative variants would need a different embedding step.
	PositionEmbeddingType string `json:"position_embedding_type"`
}

// supportedModels are the architectures this encoder implements.
//
// They are all the plain BERT encoder: word+position+token-type embeddings, then
// pre-norm-free post-norm layers of self-attention and a GELU feed-forward. The
// popular sentence-embedding models are all in this family (all-MiniLM,
// bge-*, gte-*, e5-*, jina-v2-*, snowflake-arctic-embed). Anything else —
// Gemma, MPNet, DeBERTa, ModernBERT, RoBERTa — is refused at load.
var supportedModels = map[string]bool{
	"bert": true,
}

type bertModel struct {
	cfg  bertConfig
	tok  *tokenizer
	pool PoolingMode
	w    map[string]*tensor

	headDim float64
	scale   float32
}

// loadModel reads a downloaded model directory.
func loadModel(dir string, pool PoolingMode) (*bertModel, error) {
	cb, err := os.ReadFile(dir + "/config.json")
	if err != nil {
		return nil, fmt.Errorf("config.json: %w", err)
	}
	var cfg bertConfig
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return nil, fmt.Errorf("config.json: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	tok, err := loadTokenizer(dir, maxLenFor(cfg))
	if err != nil {
		return nil, err
	}
	weights, err := readSafetensors(dir + "/model.safetensors")
	if err != nil {
		return nil, err
	}
	// Sentence-transformers exports drop the `bert.` prefix; the plain
	// transformers export keeps it. Normalising the names here means every
	// lookup below is prefix-free.
	for name, t := range weights {
		if base, ok := strings.CutPrefix(name, "bert."); ok {
			if _, exists := weights[base]; !exists {
				weights[base] = t
			}
		}
	}
	headDim := float64(cfg.HiddenSize) / float64(cfg.NumAttentionHeads)
	m := &bertModel{
		cfg: cfg, tok: tok, pool: pool, w: weights,
		headDim: headDim,
		scale:   float32(1 / math.Sqrt(headDim)),
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	return m, nil
}

// maxLenFor caps the sequence at the model's own position limit and at a
// ceiling. Memory text is short, and attention cost grows with the square of
// the sequence length, so a 512-token cap keeps an embed well under a second on
// one core while comfortably fitting a memory plus its title.
func maxLenFor(cfg bertConfig) int {
	const ceiling = 512
	n := cfg.MaxPositionEmbeddings
	if n <= 0 || n > ceiling {
		n = ceiling
	}
	return n
}

func validateConfig(cfg bertConfig) error {
	if !supportedModels[cfg.ModelType] {
		return fmt.Errorf(
			"model_type %q is not supported: this runs BERT-family encoders only "+
				"(bert; e.g. all-MiniLM-L6-v2, bge-*, gte-*, e5-*, nomic-embed-text-v1.5, jina-embeddings-v2). "+
				"Gemma, MPNet, DeBERTa, ModernBERT and RoBERTa need a different encoder",
			cfg.ModelType)
	}
	if cfg.HiddenSize <= 0 || cfg.NumHiddenLayers <= 0 || cfg.NumAttentionHeads <= 0 {
		return fmt.Errorf("config.json is missing hidden_size/num_hidden_layers/num_attention_heads")
	}
	if cfg.HiddenSize%cfg.NumAttentionHeads != 0 {
		return fmt.Errorf("hidden_size %d is not divisible by num_attention_heads %d", cfg.HiddenSize, cfg.NumAttentionHeads)
	}
	if cfg.PositionEmbeddingType != "" && cfg.PositionEmbeddingType != "absolute" {
		return fmt.Errorf("position_embedding_type %q is not supported (only absolute)", cfg.PositionEmbeddingType)
	}
	switch cfg.HiddenAct {
	case "", "gelu", "gelu_new":
		// gelu_new is the tanh approximation and is handled at the call site.
	default:
		return fmt.Errorf("hidden_act %q is not supported (only gelu)", cfg.HiddenAct)
	}
	return nil
}

// check verifies every weight the forward pass reads is present, so a truncated
// download fails at load with a named tensor rather than as a zero vector.
func (m *bertModel) check() error {
	need := []string{
		"embeddings.word_embeddings.weight",
		"embeddings.position_embeddings.weight",
		"embeddings.token_type_embeddings.weight",
		"embeddings.LayerNorm.weight",
		"embeddings.LayerNorm.bias",
	}
	for i := 0; i < m.cfg.NumHiddenLayers; i++ {
		p := fmt.Sprintf("encoder.layer.%d.", i)
		need = append(need,
			p+"attention.self.query.weight", p+"attention.self.query.bias",
			p+"attention.self.key.weight", p+"attention.self.key.bias",
			p+"attention.self.value.weight", p+"attention.self.value.bias",
			p+"attention.output.dense.weight", p+"attention.output.dense.bias",
			p+"attention.output.LayerNorm.weight", p+"attention.output.LayerNorm.bias",
			p+"intermediate.dense.weight", p+"intermediate.dense.bias",
			p+"output.dense.weight", p+"output.dense.bias",
			p+"output.LayerNorm.weight", p+"output.LayerNorm.bias",
		)
	}
	for _, name := range need {
		if _, ok := m.w[name]; !ok {
			return fmt.Errorf("model is missing tensor %q (unsupported architecture?)", name)
		}
	}
	return nil
}

func (m *bertModel) get(name string) *tensor { return m.w[name] }

// Embed runs the encoder and returns one L2-normalised vector.
func (m *bertModel) Embed(text string) ([]float32, error) {
	ids := m.tok.Encode(text)
	if len(ids) == 0 {
		return nil, fmt.Errorf("nothing to embed")
	}
	h := m.forward(ids)
	out := m.poolTokens(h, len(ids))
	normalize(out)
	return out, nil
}

// forward runs the embeddings plus every encoder layer, returning the hidden
// states laid out as [tokens, hidden].
func (m *bertModel) forward(ids []int32) []float32 {
	n := len(ids)
	d := m.cfg.HiddenSize

	h := make([]float32, n*d)
	word := m.get("embeddings.word_embeddings.weight")
	pos := m.get("embeddings.position_embeddings.weight")
	typ := m.get("embeddings.token_type_embeddings.weight")
	for i, id := range ids {
		wi := int(id) * d
		if wi < 0 || wi+d > len(word.data) {
			wi = 0 // unknown id — the vocabulary and config disagree
		}
		pi := i * d
		if pi+d > len(pos.data) {
			pi = 0 // longer than the model's position table; the tokenizer caps it
		}
		dst := h[i*d : (i+1)*d]
		copy(dst, word.data[wi:wi+d])
		for j := 0; j < d; j++ {
			dst[j] += pos.data[pi+j]
			if len(typ.data) >= d {
				dst[j] += typ.data[j] // segment 0 for a single sequence
			}
		}
	}
	layerNorm(h, n, d, m.get("embeddings.LayerNorm.weight"), m.get("embeddings.LayerNorm.bias"), m.cfg.LayerNormEps)

	// Scratch buffers are allocated once and reused across layers: twelve layers
	// of a 768-wide model would otherwise allocate tens of megabytes per embed.
	q := make([]float32, n*d)
	k := make([]float32, n*d)
	v := make([]float32, n*d)
	ctx := make([]float32, n*d)
	att := make([]float32, n*d)
	ffn := make([]float32, n*m.cfg.IntermediateSize)
	heads := m.cfg.NumAttentionHeads
	hd := int(m.headDim)

	for l := 0; l < m.cfg.NumHiddenLayers; l++ {
		p := fmt.Sprintf("encoder.layer.%d.", l)
		linear(h, n, m.get(p+"attention.self.query.weight"), m.get(p+"attention.self.query.bias"), q)
		linear(h, n, m.get(p+"attention.self.key.weight"), m.get(p+"attention.self.key.bias"), k)
		linear(h, n, m.get(p+"attention.self.value.weight"), m.get(p+"attention.self.value.bias"), v)

		// ctx accumulates each head's output, and the buffer is reused across
		// layers, so it must start clean. Without this the previous layer's
		// attention output bleeds into this one.
		clear(ctx)
		attention(q, k, v, ctx, att, n, heads, hd, m.scale)

		linear(ctx, n, m.get(p+"attention.output.dense.weight"), m.get(p+"attention.output.dense.bias"), att)
		addInPlace(h, att)
		layerNorm(h, n, d, m.get(p+"attention.output.LayerNorm.weight"), m.get(p+"attention.output.LayerNorm.bias"), m.cfg.LayerNormEps)

		linear(h, n, m.get(p+"intermediate.dense.weight"), m.get(p+"intermediate.dense.bias"), ffn)
		if m.cfg.HiddenAct == "gelu_new" {
			geluTanhInPlace(ffn)
		} else {
			geluInPlace(ffn)
		}
		linear(ffn, n, m.get(p+"output.dense.weight"), m.get(p+"output.dense.bias"), att)
		addInPlace(h, att)
		layerNorm(h, n, d, m.get(p+"output.LayerNorm.weight"), m.get(p+"output.LayerNorm.bias"), m.cfg.LayerNormEps)
	}
	return h
}

// poolTokens reduces the hidden states to one vector. There is no padding — a
// single sequence is encoded at a time — so the mean is over every token.
func (m *bertModel) poolTokens(h []float32, n int) []float32 {
	d := m.cfg.HiddenSize
	out := make([]float32, d)
	if m.pool == PoolCLS {
		copy(out, h[:d])
		return out
	}
	for i := 0; i < n; i++ {
		row := h[i*d : (i+1)*d]
		for j, x := range row {
			out[j] += x
		}
	}
	inv := float32(1) / float32(n)
	for j := range out {
		out[j] *= inv
	}
	return out
}

// linear computes out = x·Wᵀ + b for every token.
//
// The loop order is deliberate: the weight row is the outer loop so it is read
// once and reused across all tokens, and the token row is the inner loop so it
// stays in cache. The naive order (tokens outer) re-streams the whole weight
// matrix per token and is several times slower on a long sequence.
func linear(x []float32, n int, w, b *tensor, out []float32) {
	in, outDim := w.cols(), w.rows()
	for j := 0; j < outDim; j++ {
		wj := w.data[j*in : (j+1)*in]
		bias := float32(0)
		if b != nil && j < len(b.data) {
			bias = b.data[j]
		}
		for i := 0; i < n; i++ {
			xi := x[i*in : (i+1)*in]
			out[i*outDim+j] = dot(wj, xi) + bias
		}
	}
}

// dot is unrolled by four: this is the innermost loop of the whole model, so the
// branch and index overhead removed here is most of the runtime.
func dot(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < n; i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1) + (s2 + s3)
}

// attention computes multi-head self-attention. Heads are the interleaved
// slices of each token's hidden vector, which is how the reference lays them out.
func attention(q, k, v, ctx, scores []float32, n, heads, hd int, scale float32) {
	d := heads * hd
	for head := 0; head < heads; head++ {
		off := head * hd
		for i := 0; i < n; i++ {
			qi := q[i*d+off : i*d+off+hd]
			row := scores[i*n : (i+1)*n]
			// Scores against every key, then a max-subtracted softmax for
			// numerical stability.
			max := float32(math.Inf(-1))
			for j := 0; j < n; j++ {
				s := dot(qi, k[j*d+off:j*d+off+hd]) * scale
				row[j] = s
				if s > max {
					max = s
				}
			}
			var sum float32
			for j := 0; j < n; j++ {
				e := float32(math.Exp(float64(row[j] - max)))
				row[j] = e
				sum += e
			}
			if sum == 0 {
				sum = 1
			}
			inv := 1 / sum
			dst := ctx[i*d+off : i*d+off+hd]
			for j := 0; j < n; j++ {
				w := row[j] * inv
				if w == 0 {
					continue
				}
				vj := v[j*d+off : j*d+off+hd]
				for t := 0; t < hd; t++ {
					dst[t] += w * vj[t]
				}
			}
		}
	}
}

// layerNorm normalises each token's vector, then applies the learned scale and
// shift. eps comes from config.json (1e-12 for BERT) and is not interchangeable
// with the more common 1e-5 — using the wrong one shifts every embedding.
func layerNorm(x []float32, n, d int, w, b *tensor, eps float64) {
	for i := 0; i < n; i++ {
		row := x[i*d : (i+1)*d]
		var mean float32
		for _, v := range row {
			mean += v
		}
		mean /= float32(d)
		var variance float32
		for _, v := range row {
			diff := v - mean
			variance += diff * diff
		}
		variance /= float32(d)
		inv := float32(1 / math.Sqrt(float64(variance)+eps))
		for j := range row {
			row[j] = (row[j]-mean)*inv*w.data[j] + b.data[j]
		}
	}
}

func addInPlace(dst, src []float32) {
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := 0; i < n; i++ {
		dst[i] += src[i]
	}
}

// geluInPlace is the exact erf-based GELU that `hidden_act: gelu` means.
func geluInPlace(x []float32) {
	const invSqrt2 = 0.7071067811865476
	for i, v := range x {
		x[i] = 0.5 * v * (1 + float32(math.Erf(float64(v)*invSqrt2)))
	}
}

// geluTanhInPlace is the tanh approximation behind `hidden_act: gelu_new`,
// which BERT exports use interchangeably with the exact form.
func geluTanhInPlace(x []float32) {
	const c = 0.7978845608028654 // sqrt(2/pi)
	for i, v := range x {
		inner := c * (v + 0.044715*v*v*v)
		x[i] = 0.5 * v * (1 + float32(math.Tanh(float64(inner))))
	}
}

// normalize scales a vector to unit length, which is what makes cosine
// similarity the right comparison for the stored vectors.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
