package store

import (
	"testing"
	"time"
)

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
	if err := s.SaveCompactionSnapshot(p.ID, "sess1", "first summary", 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCompactionSnapshot(p.ID, "sess1", "latest summary", 12); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCompactionSnapshot(p.ID, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "latest summary" || got.CoveredMessageID != 12 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestUsersAndOwnership(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	alice := &User{ID: NewID(), Username: "alice", PasswordHash: "h1"}
	bob := &User{ID: NewID(), Username: "bob", PasswordHash: "h2"}
	if err := s.CreateUser(alice); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(bob); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(&User{ID: NewID(), Username: "alice", PasswordHash: "x"}); err != ErrConflict {
		t.Fatalf("duplicate username: err = %v, want ErrConflict", err)
	}

	u, err := s.GetUserByUsername("alice")
	if err != nil || u.ID != alice.ID {
		t.Fatalf("GetUserByUsername = %#v, %v", u, err)
	}
	if _, err := s.GetUserByUsername("nobody"); err != ErrNotFound {
		t.Fatalf("missing user: err = %v, want ErrNotFound", err)
	}
	if n, err := s.UserCount(); err != nil || n != 2 {
		t.Fatalf("UserCount = %d, %v", n, err)
	}

	pa := &Project{ID: NewID(), Name: "a", Path: t.TempDir(), OwnerID: alice.ID}
	pb := &Project{ID: NewID(), Name: "b", Path: t.TempDir(), OwnerID: bob.ID}
	if err := s.CreateProject(pa); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(pb); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProjectsByOwner(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != pa.ID {
		t.Fatalf("alice projects = %d entries, want only hers", len(list))
	}
	if all, err := s.ListProjects(); err != nil || len(all) != 2 {
		t.Fatalf("ListProjects = %d entries, %v", len(all), err)
	}

	// User settings layer on top of global settings.
	if err := s.SetSetting("llm_api_key", "global"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserSetting(alice.ID, "llm_api_key", "alice-key"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.GetUserSetting(alice.ID, "llm_api_key"); !ok || v != "alice-key" {
		t.Fatalf("alice user setting = %q, %v", v, ok)
	}
	// GetUserSetting is strictly per-user; the fallback to global values
	// happens in the server's userSetting helper (see TestSettingsAreUserScoped).
	if _, ok, _ := s.GetUserSetting(bob.ID, "llm_api_key"); ok {
		t.Fatal("bob has no user-level setting — the server falls back for him")
	}
	if err := s.DeleteUserSetting(alice.ID, "llm_api_key"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetUserSetting(alice.ID, "llm_api_key"); ok {
		t.Fatal("after delete alice should have no user-level setting")
	}

	// Sessions resolve to the owning user; orphaned (NULL user) and expired
	// rows die on lookup.
	token := "tok1"
	if err := s.CreateSession(token, alice.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if uid, ok, _ := s.SessionUser(token); !ok || uid != alice.ID {
		t.Fatalf("SessionUser = %q, %v", uid, ok)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (token, created_at, expires_at) VALUES ('orphan', 1, 9999999999)`); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SessionUser("orphan"); ok {
		t.Fatal("orphan (NULL user) session should be invalid")
	}
	if err := s.DeleteSession(token); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SessionUser(token); ok {
		t.Fatal("deleted session should be invalid")
	}

	// Deleting a user removes their projects too.
	if err := s.DeleteUser(alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByID(alice.ID); err != ErrNotFound {
		t.Fatalf("deleted user still present: %v", err)
	}
	if _, err := s.GetProject(pa.ID); err != ErrNotFound {
		t.Fatalf("alice's project still present: %v", err)
	}
	if _, err := s.GetProject(pb.ID); err != nil {
		t.Fatalf("bob's project should survive: %v", err)
	}
	if n, _ := s.UserCount(); n != 1 {
		t.Fatalf("UserCount = %d after delete, want 1", n)
	}
}

func TestMigrateLegacyPasswordBecomesAdmin(t *testing.T) {
	dir := t.TempDir()
	// Build a pre-multi-user database by hand: a stored password and an
	// ownerless project, then reopen through Open to run the migration.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("password_hash", "hash123"); err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: NewID(), Name: "legacy", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.UserCount(); n != 1 {
		t.Fatalf("expected 1 admin after migration, got %d", n)
	}
	admin, err := s2.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if !admin.IsAdmin || admin.PasswordHash != "hash123" {
		t.Fatalf("admin = %#v", admin)
	}
	if _, ok, _ := s2.GetSetting("password_hash"); ok {
		t.Fatal("legacy password_hash should be consumed")
	}
	// The ownerless project was claimed by the admin.
	got, err := s2.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID != admin.ID {
		t.Fatalf("legacy project owner = %q, want %q", got.OwnerID, admin.ID)
	}
}

func TestMigrateLegacySessionDropped(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an old anonymous session row.
	if _, err := s.db.Exec(`INSERT INTO sessions (token, created_at, expires_at) VALUES ('anon', 1, 9999999999)`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok, _ := s2.SessionUser("anon"); ok {
		t.Fatal("anonymous session should be dropped by migration")
	}
	// The migration must be idempotent — reopening again is fine (covered by
	// Open running migrate on every start; TestMigrateLegacyPasswordBecomesAdmin
	// already reopens).
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
}

func TestMergeContinuedTurn(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &Project{ID: NewID(), Name: "t", Path: t.TempDir()}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	sid, err := s.EnsureDefaultSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := s.AddMessage(p.ID, sid.ID, "user", "hello", "", "", "", "", "")
	partialID, _ := s.AddMessage(p.ID, sid.ID, "assistant", "Partial reply", "", "", "thinking", "", "")
	_, _ = s.AddMessage(p.ID, sid.ID, "error", "boom", "", "", "", "", "")
	contID, _ := s.AddMessage(p.ID, sid.ID, "assistant", " continuation text", "", "", "more thinking", `{"input":1}`, "")
	errID, _ := s.AddMessage(p.ID, sid.ID, "error", "boom2", "", "", "", "", "")

	merged, err := s.MergeContinuedTurn(p.ID, sid.ID, partialID)
	if err != nil {
		t.Fatal(err)
	}
	if merged != partialID {
		t.Fatalf("merged = %d", merged)
	}
	msgs, err := s.ListMessages(p.ID, sid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 { // user, merged partial, (no error/continuation)
		t.Fatalf("messages after merge: %d", len(msgs))
	}
	partial, _ := s.GetMessage(p.ID, partialID)
	if partial.Content != "Partial reply\n\ncontinuation text" {
		t.Fatalf("merged content = %q", partial.Content)
	}
	if partial.Reasoning != "thinking\nmore thinking" {
		t.Fatalf("merged reasoning = %q", partial.Reasoning)
	}
	if partial.Usage != `{"input":1}` {
		t.Fatalf("merged usage = %q", partial.Usage)
	}
	for _, id := range []int64{contID, errID} {
		if _, err := s.GetMessage(p.ID, id); err == nil {
			t.Fatalf("message %d should be gone", id)
		}
	}
	_ = userID
}
