package server

import (
	"os"
	"testing"
	"time"

	"v1/internal/embed"
)

// TestNativeBackfillEmbedsMemories runs the real backfill path against a native
// model on disk.
//
// The embed package proves the vectors are right; this proves they reach the
// store. That link is the one that fails silently: a memory with no vector is
// still found lexically, so retrieval keeps working while semantic search
// quietly does nothing at all.
//
// Gated on V1_EMBED_TEST_MODEL because it needs real weights; the reference
// parity test in internal/embed is what runs without them.
func TestNativeBackfillEmbedsMemories(t *testing.T) {
	dir := os.Getenv("V1_EMBED_TEST_MODEL")
	if dir == "" {
		t.Skip("set V1_EMBED_TEST_MODEL to a directory holding config.json and model.safetensors")
	}
	st, s, pid := memTestServer(t)
	mustAdd(t, st, pid, "the deploy script lives in scripts/deploy.sh", "fact", 1)
	if err := st.SetSetting(keyEmbedProvider, embed.ProviderNative); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(keyEmbedModel, dir); err != nil {
		t.Fatal(err)
	}

	s.backfillEmbeddings(pid, "")

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ids, err := st.MemoriesMissingEmbeddings(pid, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the backfill never embedded the memory")
}
