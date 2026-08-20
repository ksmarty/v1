package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"v1/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st, false, ""), st
}

func TestSetupAndLogin(t *testing.T) {
	m, _ := newManager(t)
	if !m.SetupRequired() {
		t.Fatal("expected setup to be required with no users")
	}
	u, err := m.CreateUser("alice", "s3cret", true)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin {
		t.Fatal("first user should be admin")
	}
	if m.SetupRequired() {
		t.Fatal("setup should no longer be required")
	}
	if _, ok := m.Login("alice", "s3cret"); !ok {
		t.Fatal("valid credentials rejected")
	}
	if _, ok := m.Login("alice", "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := m.Login("bob", "s3cret"); ok {
		t.Fatal("unknown user accepted")
	}
	if err := m.SetPassword(u.ID, "newpass"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Login("alice", "newpass"); !ok {
		t.Fatal("changed password rejected")
	}
	if _, ok := m.Login("alice", "s3cret"); ok {
		t.Fatal("old password still valid")
	}
}

func TestBootstrapAdmin(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewManager(st, false, "envpw")
	m.BootstrapAdmin()
	if m.SetupRequired() {
		t.Fatal("env password should satisfy setup")
	}
	u, ok := m.Login("admin", "envpw")
	if !ok {
		t.Fatal("env-bootstrapped admin login failed")
	}
	if !u.IsAdmin {
		t.Fatal("env-bootstrapped user should be admin")
	}
	// Idempotent: a second bootstrap leaves the existing user alone.
	m2 := NewManager(st, false, "otherpw")
	m2.BootstrapAdmin()
	if _, ok := m2.Login("admin", "envpw"); !ok {
		t.Fatal("second bootstrap clobbered the admin password")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	m, _ := newManager(t)
	u, err := m.CreateUser("alice", "pw", false)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	if err := m.CreateSession(rr, req, u.ID); err != nil {
		t.Fatal(err)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}

	authed := httptest.NewRequest("GET", "/", nil)
	authed.AddCookie(cookies[0])
	got, ok := m.User(authed)
	if !ok || got.Username != "alice" {
		t.Fatalf("User = %#v, %v", got, ok)
	}
	if !m.Authenticated(authed) {
		t.Fatal("session should authenticate")
	}

	// A deleted user invalidates their sessions.
	if err := m.st.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.User(authed); ok {
		t.Fatal("session of a deleted user should be invalid")
	}

	// Logging out clears the cookie and kills the session.
	bob, err := m.CreateUser("bob", "pw", false)
	if err != nil {
		t.Fatal(err)
	}
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/", nil)
	if err := m.CreateSession(rr2, req2, bob.ID); err != nil {
		t.Fatal(err)
	}
	dest := httptest.NewRecorder()
	m.DestroySession(dest, req2)
	if _, ok := m.User(req2); ok {
		t.Fatal("session should be invalid after logout")
	}
	if len(dest.Result().Cookies()) == 0 || dest.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout should clear the cookie")
	}
}

func TestMiddlewareAttachesUser(t *testing.T) {
	m, _ := newManager(t)
	u, err := m.CreateUser("alice", "pw", false)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	if err := m.CreateSession(rr, req, u.ID); err != nil {
		t.Fatal(err)
	}
	cookie := rr.Result().Cookies()[0]

	var seen *store.User
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	authed := httptest.NewRequest("GET", "/api/x", nil)
	authed.AddCookie(cookie)
	h.ServeHTTP(httptest.NewRecorder(), authed)
	if seen == nil || seen.Username != "alice" {
		t.Fatalf("middleware did not attach user: %#v", seen)
	}

	// Unauthenticated requests are rejected before the handler.
	seen = nil
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/x", nil))
	if seen != nil {
		t.Fatal("unauthenticated request reached the handler")
	}

	// The SPA shell (non-/api, non-/preview) is public so the app can mount
	// and route to /login — no session required.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("SPA shell should be served without auth, got %d", rr.Code)
	}
	seen = nil
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/assets/index-abc.js", nil))
	if seen != nil {
		t.Fatal("asset request should not carry a user without a session")
	}

	// With a session, the SPA shell still attaches the user.
	seen = nil
	withSess := httptest.NewRequest("GET", "/", nil)
	withSess.AddCookie(cookie)
	h.ServeHTTP(httptest.NewRecorder(), withSess)
	if seen == nil || seen.Username != "alice" {
		t.Fatalf("authenticated SPA shell should attach user: %#v", seen)
	}
}

func TestDisabledManagerAllowsAll(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewManager(st, true, "")
	req := httptest.NewRequest("GET", "/", nil)
	u, ok := m.User(req)
	if !ok || u.ID != "local" {
		t.Fatalf("disabled manager user = %#v, %v", u, ok)
	}
}
