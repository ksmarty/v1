package store

import "testing"

// The dashboard shows when a session was last worked in, so the session list has
// to carry the last assistant message's time rather than only the creation time.
func TestListSessionsReportsLastTurn(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := &Project{ID: NewID(), Name: "test", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	cs, err := s.CreateChatSession(p.ID, "chat")
	if err != nil {
		t.Fatal(err)
	}

	// A session that has never had a turn has nothing to report.
	got, err := s.ListSessions(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LastTurnAt != 0 {
		t.Fatalf("fresh session = %+v, want LastTurnAt 0", got)
	}

	// The user's own message is not the agent finishing, so it must not move it.
	if _, err := s.AddMessage(p.ID, cs.ID, "user", "hi", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ListSessions(p.ID); got[0].LastTurnAt != 0 {
		t.Fatalf("LastTurnAt after a user message = %d, want 0", got[0].LastTurnAt)
	}

	if _, err := s.AddMessage(p.ID, cs.ID, "assistant", "hello", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListSessions(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].LastTurnAt == 0 {
		t.Fatal("LastTurnAt = 0 after an assistant message, want a timestamp")
	}
}
