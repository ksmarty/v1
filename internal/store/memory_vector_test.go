package store

import (
	"math"
	"testing"
)

// A memory's vector has to survive the BLOB round trip exactly: the ranker
// compares it against a query vector, and a corrupted float would silently
// produce a wrong similarity rather than an error.
func TestMemoryEmbeddingRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddMemory(p.ID, "the cache key format", "fact", 1)
	if err != nil {
		t.Fatal(err)
	}

	// Before an embedding provider exists the memory has no vector, and must
	// report that rather than an empty one that would compare as similar to
	// everything.
	if vs, err := s.MemoryVectors(p.ID); err != nil || len(vs) != 0 {
		t.Fatalf("vectors before embedding = %v, %v; want none", vs, err)
	}

	want := []float32{0.25, -1.5, 3, 0}
	if err := s.SetMemoryEmbedding(id, "test-model", want); err != nil {
		t.Fatal(err)
	}
	vs, err := s.MemoryVectors(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := vs[id]
	if !ok {
		t.Fatal("the memory has no stored vector")
	}
	if len(got) != len(want) {
		t.Fatalf("vector length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("vector[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// A model swap makes stored vectors incomparable, so the backfill has to be able
// to find them: comparing a 768-wide vector against a 1536-wide one yields 0,
// which looks like "unrelated" rather than "wrong model".
func TestMemoriesMissingEmbeddingsDetectsModelChange(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	embedded, err := s.AddMemory(p.ID, "has a vector", "fact", 1)
	if err != nil {
		t.Fatal(err)
	}
	never, err := s.AddMemory(p.ID, "never embedded", "fact", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemoryEmbedding(embedded, "model-a", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	missing, err := s.MemoriesMissingEmbeddings(p.ID, "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != never {
		t.Fatalf("missing = %v, want just the never-embedded memory %d", missing, never)
	}

	// A different configured model invalidates the existing vector too.
	missing, err = s.MemoriesMissingEmbeddings(p.ID, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing after a model change = %v, want both memories", missing)
	}
}

// Tags are part of the retrieval signal, so they have to round trip.
func TestMemoryTagsRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddMemory(p.ID, "the session list is built in Projects.tsx", "fact", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemoryTags(id, "ui, sessions, react"); err != nil {
		t.Fatal(err)
	}
	mems, err := s.ListMemories(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Tags != "ui, sessions, react" {
		t.Fatalf("tags = %q", mems[0].Tags)
	}
}

// A memory stored before this change has an empty tags column rather than NULL,
// so listing must not fail on the old rows.
func TestListMemoriesOnAPreMigrationDatabase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMemory(p.ID, "an old memory", "fact", 1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopening runs the migrations again, which must be idempotent.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	mems, err := s2.ListMemories(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Tags != "" {
		t.Fatalf("memories after reopening = %+v", mems)
	}
}
