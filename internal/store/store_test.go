package store

import "testing"

func TestCompactionSnapshotRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCompactionSnapshot(p.ID, "first summary", 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCompactionSnapshot(p.ID, "latest summary", 12); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCompactionSnapshot(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "latest summary" || got.CoveredMessageID != 12 {
		t.Fatalf("snapshot = %#v", got)
	}
}
