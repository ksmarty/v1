package store

import "testing"

// A fork must carry the whole transcript, leave the source untouched, and keep
// the compaction summary pointing at the copy's matching message — its covered
// id belongs to the source session, so a straight copy would point at a row that
// does not exist (or, worse, at an unrelated row with the same id).
func TestForkChatSessionCopiesTranscript(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	src, err := s.CreateChatSession(p.ID, "chat")
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := s.AddMessage(p.ID, src.ID, "user", "first", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(p.ID, src.ID, "assistant", "answer", `{"tool_calls":[]}`, "m", "r", `{"total":1}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(p.ID, src.ID, "user", "second", "", "", "", "", "[{\"name\":\"a.txt\"}]"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCompactionSnapshot(p.ID, src.ID, "summary", firstID); err != nil {
		t.Fatal(err)
	}

	forked, err := s.ForkChatSession(p.ID, src.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if forked.Name != "chat (fork)" {
		t.Fatalf("fork name = %q, want %q", forked.Name, "chat (fork)")
	}

	got, err := s.ListMessages(p.ID, forked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("fork messages = %d, want 3", len(got))
	}
	want := []string{"first", "answer", "second"}
	for i, m := range got {
		if m.Content != want[i] {
			t.Fatalf("fork message %d content = %q, want %q", i, m.Content, want[i])
		}
		if m.ID == firstID || m.ID <= 0 {
			t.Fatalf("fork message %d reused a source id (%d)", i, m.ID)
		}
	}
	if got[1].ToolJSON != `{"tool_calls":[]}` || got[1].Model != "m" || got[1].Reasoning != "r" || got[1].Usage != `{"total":1}` {
		t.Fatalf("fork dropped an assistant payload: %+v", got[1])
	}
	if got[2].Attachments != `[{"name":"a.txt"}]` {
		t.Fatalf("fork dropped attachments: %q", got[2].Attachments)
	}

	snap, err := s.GetCompactionSnapshot(p.ID, forked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Summary != "summary" {
		t.Fatalf("fork summary = %q", snap.Summary)
	}
	if snap.CoveredMessageID != got[0].ID {
		t.Fatalf("fork covered id = %d, want %d (the copy of the covered message)", snap.CoveredMessageID, got[0].ID)
	}

	// The source keeps its own transcript and snapshot.
	orig, err := s.ListMessages(p.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(orig) != 3 || orig[0].ID != firstID {
		t.Fatalf("source transcript changed: %+v", orig)
	}
	origSnap, err := s.GetCompactionSnapshot(p.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if origSnap.CoveredMessageID != firstID {
		t.Fatalf("source covered id = %d, want %d", origSnap.CoveredMessageID, firstID)
	}

	// An explicit name wins.
	named, err := s.ForkChatSession(p.ID, src.ID, "other direction")
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "other direction" {
		t.Fatalf("named fork = %q", named.Name)
	}
}

// A fork of a missing session is ErrNotFound, not a new empty chat.
func TestForkChatSessionUnknown(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForkChatSession(p.ID, "nope", ""); err != ErrNotFound {
		t.Fatalf("fork unknown session error = %v, want ErrNotFound", err)
	}
}
