package store

import (
	"testing"
)

// TestMessageCheckpoint verifies the checkpoint column is migrated in and the
// stamping helpers round-trip on a real conversation.
func TestMessageCheckpoint(t *testing.T) {
	s, err := Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	proj := &Project{ID: NewID(), Name: "cp", Path: t.TempDir()}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	// A column added by migrateAddColumns must be usable immediately.
	uid, err := s.AddMessage(proj.ID, "sess", "user", "build it", "", "m", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(proj.ID, "sess", "assistant", "done", "", "m", "", "", ""); err != nil {
		t.Fatal(err)
	}
	// Wait, AddMessage has no checkpoint param — stamp after the fact, as the
	// server does.
	if err := s.SetMessageCheckpoint(uid, "deadbeef1234"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestUserMessageID(proj.ID, "sess")
	if err != nil || got != uid {
		t.Fatalf("LatestUserMessageID = %d err=%v, want %d", got, err, uid)
	}
	// Empty hash is a no-op.
	if err := s.SetMessageCheckpoint(uid, ""); err != nil {
		t.Fatal(err)
	}
	// Attach to a second user turn; latest resolves to it.
	uid2, err := s.AddMessage(proj.ID, "sess", "user", "again", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.LatestUserMessageID(proj.ID, "sess")
	if err != nil || got != uid2 {
		t.Fatalf("LatestUserMessageID = %d err=%v, want %d", got, err, uid2)
	}
	// No user rows in another session -> 0, no error.
	got, err = s.LatestUserMessageID(proj.ID, "empty-sess")
	if err != nil || got != 0 {
		t.Fatalf("empty session: got %d err=%v, want 0", got, err)
	}
}
