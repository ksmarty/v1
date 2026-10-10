// native.go runs an embedding model inside v1, with no external service.
//
// The weights come from Hugging Face on first use and are cached on disk, then
// held in memory. A user names a model by repository id or by pasting the model
// page URL; a local directory is accepted too, which is what makes the feature
// usable offline and testable without network access.
//
// Only BERT-family encoders are supported — see supportedModels in bert.go for
// the exact set and the reason the rest are refused.
package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultNativeModel is the model used when the native provider is selected
// without naming one. all-MiniLM-L6-v2 is the standard small sentence-embedding
// model: 22M parameters, 384 dimensions, and fast enough to embed a memory
// inline on a single core.
const DefaultNativeModel = "sentence-transformers/all-MiniLM-L6-v2"

// nativeFileTimeout bounds one weight download. Model files run to hundreds of
// megabytes, so this is generous, but a stalled connection must not hang a turn
// forever.
const nativeFileTimeout = 20 * time.Minute

// modelRepoRe matches a Hugging Face repository id: owner/name, with the
// characters HF allows.
var modelRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// parseModelRef resolves what the user typed into a repository id, or into a
// local directory that already holds a model.
//
// Accepted: a repo id, a huggingface.co model URL (with or without /tree/<rev>),
// an hf: prefix, or a path to a directory containing config.json.
func parseModelRef(raw string) (repo, dir string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = DefaultNativeModel
	}
	s = strings.TrimPrefix(s, "hf:")
	s = strings.TrimSuffix(s, "/")

	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u := strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		u = strings.TrimPrefix(u, "www.")
		rest, ok := strings.CutPrefix(u, "huggingface.co/")
		if !ok {
			return "", "", fmt.Errorf("only huggingface.co model links are supported, got %q", raw)
		}
		// Drop everything from /tree, /blob or /resolve onwards — the user may
		// have copied the URL from a file browser rather than the model page.
		for _, marker := range []string{"/tree/", "/blob/", "/resolve/"} {
			if i := strings.Index(rest, marker); i >= 0 {
				rest = rest[:i]
			}
		}
		s = rest
	}

	// A directory with a config.json is a model already on disk. This is what
	// lets a model be pre-placed for an offline install.
	if fi, statErr := os.Stat(s); statErr == nil && fi.IsDir() {
		if _, cErr := os.Stat(filepath.Join(s, "config.json")); cErr == nil {
			return "", s, nil
		}
		return "", "", fmt.Errorf("%s has no config.json, so it is not a model directory", s)
	}

	if !modelRepoRe.MatchString(s) {
		return "", "", fmt.Errorf(
			"%q is not a model reference: expected a Hugging Face repository id "+
				"(owner/name), a huggingface.co model link, or a local model directory", raw)
	}
	return s, "", nil
}

// modelCacheDir is where downloaded weights live. The weights are large and
// slow to fetch, so the default is under the v1 data directory, which the
// container mounts as a volume and which survives a redeploy; the OS user cache
// is the fallback for a checkout without V1_DATA_DIR. V1_MODEL_CACHE overrides
// both.
func modelCacheDir(repo string) (string, error) {
	root := os.Getenv("V1_MODEL_CACHE")
	if root == "" {
		if data := os.Getenv("V1_DATA_DIR"); data != "" {
			root = filepath.Join(data, "models")
		} else {
			base, err := os.UserCacheDir()
			if err != nil {
				base = os.TempDir()
			}
			root = filepath.Join(base, "v1", "models")
		}
	}
	if repo == "" {
		return root, nil
	}
	// A repo id has a slash in it; flatten it so the cache is one level deep.
	return filepath.Join(root, strings.ReplaceAll(repo, "/", "__")), nil
}

// nativeRunner owns one model: its files on disk and the loaded encoder.
type nativeRunner struct {
	repo   string
	dir    string
	token  string // HF token for gated repos, optional
	client *http.Client

	mu  sync.Mutex
	mod *bertModel
}

// nativeCache shares loaded models across callers. Embedding is called per turn
// and per memory, so reloading a model each time would be untenable; one entry
// per repository is all a single user needs.
var (
	nativeMu    sync.Mutex
	nativeCache = map[string]*nativeRunner{}
)

func nativeRunnerFor(repo, dir, token string) *nativeRunner {
	key := repo + "\x00" + dir
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if r, ok := nativeCache[key]; ok {
		return r
	}
	r := &nativeRunner{repo: repo, dir: dir, token: token, client: &http.Client{Timeout: nativeFileTimeout}}
	nativeCache[key] = r
	return r
}

// Loaded reports whether the model is already in memory.
func (r *nativeRunner) Loaded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mod != nil
}

// model loads the encoder, downloading its files first if needed.
func (r *nativeRunner) model() (*bertModel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mod != nil {
		return r.mod, nil
	}
	if r.dir == "" {
		dir, err := modelCacheDir(r.repo)
		if err != nil {
			return nil, err
		}
		r.dir = dir
		if err := r.ensureFiles(); err != nil {
			return nil, err
		}
	}
	pool := r.detectPooling()
	m, err := loadModel(r.dir, pool)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.describe(), err)
	}
	r.mod = m
	return m, nil
}

func (r *nativeRunner) describe() string {
	if r.repo != "" {
		return r.repo
	}
	return r.dir
}

// detectPooling reads the sentence-transformers pooling config. Plain BERT
// checkpoints have no such file; mean pooling is the right default for
// retrieval either way, and it is what the sentence-transformers models use.
func (r *nativeRunner) detectPooling() PoolingMode {
	b, err := os.ReadFile(filepath.Join(r.dir, "1_Pooling", "config.json"))
	if err != nil {
		return PoolMean
	}
	var p struct {
		CLS  bool `json:"pooling_mode_cls_token"`
		Mean bool `json:"pooling_mode_mean_tokens"`
	}
	if json.Unmarshal(b, &p) != nil {
		return PoolMean
	}
	if p.CLS && !p.Mean {
		return PoolCLS
	}
	return PoolMean
}

// requiredFiles are the files a BERT encoder needs. tokenizer.json and vocab.txt
// are alternatives, so either satisfies the tokenizer requirement.
func (r *nativeRunner) requiredFiles() []string {
	return []string{"config.json"}
}

// ensureFiles downloads whatever the model directory is missing. Each file is
// written to a temporary name and renamed, so an interrupted download can never
// be mistaken for a complete one on the next run.
func (r *nativeRunner) ensureFiles() error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	for _, f := range r.requiredFiles() {
		if err := r.fetch(f, true); err != nil {
			return err
		}
	}
	// The tokenizer is required but has two accepted forms; try them in order
	// and accept whichever the repository actually publishes.
	if !fileExists(filepath.Join(r.dir, "tokenizer.json")) && !fileExists(filepath.Join(r.dir, "vocab.txt")) {
		if err := r.fetch("tokenizer.json", true); err != nil {
			if err2 := r.fetch("vocab.txt", true); err2 != nil {
				return fmt.Errorf("no tokenizer in this repository (neither tokenizer.json nor vocab.txt)")
			}
		}
	}
	// Optional files: absent is fine, a real error is not.
	for _, f := range []string{"1_Pooling/config.json", "tokenizer_config.json"} {
		_ = r.fetch(f, false)
	}
	return r.ensureWeights()
}

// ensureWeights fetches model.safetensors, following the shard index when the
// repository publishes a model split across files.
func (r *nativeRunner) ensureWeights() error {
	if fileExists(filepath.Join(r.dir, "model.safetensors")) {
		return nil
	}
	index := filepath.Join(r.dir, "model.safetensors.index.json")
	if err := r.fetch("model.safetensors.index.json", false); err == nil && fileExists(index) {
		var idx struct {
			WeightMap map[string]string `json:"weight_map"`
		}
		b, err := os.ReadFile(index)
		if err != nil || json.Unmarshal(b, &idx) != nil {
			return fmt.Errorf("model.safetensors.index.json is unreadable")
		}
		seen := map[string]bool{}
		for _, shard := range idx.WeightMap {
			if seen[shard] {
				continue
			}
			seen[shard] = true
			if err := r.fetch(shard, true); err != nil {
				return err
			}
		}
		if len(seen) == 0 {
			return fmt.Errorf("model.safetensors.index.json lists no shards")
		}
		return nil
	}
	if err := r.fetch("model.safetensors", true); err != nil {
		return fmt.Errorf("no weights in this repository: %w", err)
	}
	return nil
}

// fetch downloads one repository file into the model directory. Missing optional
// files are not an error.
func (r *nativeRunner) fetch(name string, required bool) error {
	dst := filepath.Join(r.dir, filepath.FromSlash(name))
	if fileExists(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s/resolve/main/%s", hfEndpoint(), r.repo, name)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if required {
			return fmt.Errorf("download %s: %w", name, err)
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if required {
			return fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
		}
		return nil
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Size() > 0
}

// hfEndpoint allows a mirror (or a local proxy) to stand in for huggingface.co.
func hfEndpoint() string {
	if v := strings.TrimRight(os.Getenv("V1_HF_ENDPOINT"), "/"); v != "" {
		return v
	}
	return "https://huggingface.co"
}

// nativeEmbed embeds a batch with the native runner, loading the model on first
// use.
func nativeEmbed(ctx context.Context, cfg Config, texts []string) ([][]float32, error) {
	repo, dir, err := parseModelRef(cfg.Model)
	if err != nil {
		return nil, err
	}
	if repo == "" {
		repo = cfg.Model
	}
	r := nativeRunnerFor(repo, dir, cfg.APIKey)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := r.model()
	if err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v, err := m.Embed(t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// PrepareNativeModel downloads and loads a model without embedding anything, so
// the settings page can validate a repository and report its width before the
// user commits to it.
func PrepareNativeModel(ctx context.Context, cfg Config) (int, error) {
	repo, dir, err := parseModelRef(cfg.Model)
	if err != nil {
		return 0, err
	}
	if repo == "" {
		repo = cfg.Model
	}
	r := nativeRunnerFor(repo, dir, cfg.APIKey)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m, err := r.model()
	if err != nil {
		return 0, err
	}
	return m.cfg.HiddenSize, nil
}
