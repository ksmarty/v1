package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"v1/internal/auth"
	"v1/internal/config"
	"v1/internal/store"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	good := map[string]string{
		"":           root,
		".":          root,
		"a.txt":      filepath.Join(root, "a.txt"),
		"sub/b.txt":  filepath.Join(root, "sub", "b.txt"),
		"a/../../b":  filepath.Join(root, "b"),
		"/abs/p.txt": filepath.Join(root, "abs", "p.txt"),
	}
	for rel, want := range good {
		got, err := safeJoin(root, rel)
		if err != nil {
			t.Fatalf("safeJoin(%q) error: %v", rel, err)
		}
		if got != want {
			t.Fatalf("safeJoin(%q) = %q, want %q", rel, got, want)
		}
	}
	// Anything resolved must stay inside root even when not rejected.
	for _, rel := range []string{"../x", "../../etc/passwd", "a/../../../b"} {
		got, err := safeJoin(root, rel)
		if err != nil {
			continue
		}
		if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Fatalf("safeJoin(%q) escaped root: %q", rel, got)
		}
	}
}

// newAuthServer builds a server with auth enabled, an admin and a regular
// user, and returns session cookies for each of them.
func newAuthServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(config.Config{DataDir: t.TempDir()}, st)
	admin, err := s.auth.CreateUser("admin", "pw", true)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := s.auth.CreateUser("alice", "pw", false)
	if err != nil {
		t.Fatal(err)
	}
	session := func(u *store.User) string {
		rr := httptest.NewRecorder()
		if err := s.auth.CreateSession(rr, httptest.NewRequest("GET", "/", nil), u.ID); err != nil {
			t.Fatal(err)
		}
		return rr.Result().Cookies()[0].Value
	}
	return s, session(admin), session(alice)
}

func cookieHeader(token string) string {
	return auth.CookieName + "=" + token
}

func TestProjectOwnershipIsolation(t *testing.T) {
	s, adminCookie, aliceCookie := newAuthServer(t)
	dir := t.TempDir()
	p := &store.Project{ID: store.NewID(), Name: "secret", Path: dir, OwnerID: "nobody"}
	if err := s.st.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	// Alice (not the owner) gets 404 — not 403, to avoid leaking existence.
	req := httptest.NewRequest("GET", "/api/projects/"+p.ID, nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner GET project = %d, want 404", rr.Code)
	}

	// The owner-less project is invisible in Alice's list too.
	req = httptest.NewRequest("GET", "/api/projects", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list projects = %d", rr.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("alice sees %d projects, want 0", len(list))
	}

	// Admin also sees only their own.
	req = httptest.NewRequest("GET", "/api/projects", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin list = %d", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("admin sees %d projects, want 0 (strict isolation)", len(list))
	}

	// An owner sees their project; ownership is set on create.
	req = httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"mine"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, body %s", rr.Code, rr.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	got, err := s.st.GetProject(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID == "" {
		t.Fatal("created project has no owner")
	}

	// And the owner can read it back.
	req = httptest.NewRequest("GET", "/api/projects/"+created.ID, nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner GET project = %d", rr.Code)
	}
}

func TestUserRoutesRequireAdmin(t *testing.T) {
	s, adminCookie, aliceCookie := newAuthServer(t)

	req := httptest.NewRequest("GET", "/api/users", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin list users = %d, want 403", rr.Code)
	}

	req = httptest.NewRequest("GET", "/api/users", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin list users = %d, body %s", rr.Code, rr.Body.String())
	}
	var users []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}

	// Admins cannot delete themselves.
	adminUser, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("DELETE", "/api/users/"+adminUser.ID, nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("self-delete = %d, want 400", rr.Code)
	}
}

func TestUpdateUserPasswordAndAdmin(t *testing.T) {
	s, adminCookie, aliceCookie := newAuthServer(t)
	alice, err := s.st.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}

	// Non-admins cannot update users.
	req := httptest.NewRequest("PATCH", "/api/users/"+alice.ID, strings.NewReader(`{"password":"x"}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin update = %d, want 403", rr.Code)
	}

	// Admin promotes alice.
	req = httptest.NewRequest("PATCH", "/api/users/"+alice.ID, strings.NewReader(`{"isAdmin":true}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("promote = %d, body %s", rr.Code, rr.Body.String())
	}
	alice, _ = s.st.GetUserByUsername("alice")
	if !alice.IsAdmin {
		t.Fatal("alice should be admin now")
	}

	// Demoting a non-last admin is fine (the original admin still exists).
	req = httptest.NewRequest("PATCH", "/api/users/"+alice.ID, strings.NewReader(`{"isAdmin":false}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("demote non-last admin = %d, body %s", rr.Code, rr.Body.String())
	}
	alice, _ = s.st.GetUserByUsername("alice")
	if alice.IsAdmin {
		t.Fatal("alice should no longer be admin")
	}

	// Admins cannot demote themselves (the last-admin protection).
	admin, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("PATCH", "/api/users/"+admin.ID, strings.NewReader(`{"isAdmin":false}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("self-demote = %d, want 400", rr.Code)
	}

	// Password reset changes the login.
	req = httptest.NewRequest("PATCH", "/api/users/"+alice.ID, strings.NewReader(`{"password":"newpw"}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("password reset = %d, body %s", rr.Code, rr.Body.String())
	}
	if _, ok := s.auth.Login("alice", "newpw"); !ok {
		t.Fatal("new password rejected")
	}
	if _, ok := s.auth.Login("alice", "pw"); ok {
		t.Fatal("old password still valid")
	}
}

func TestSettingsAreUserScoped(t *testing.T) {
	s, _, aliceCookie := newAuthServer(t)
	alice, err := s.st.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}

	// Alice saves her own LLM key.
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"llm":{"apiKey":"alice-key"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("put settings = %d, body %s", rr.Code, rr.Body.String())
	}
	if v, ok, _ := s.st.GetUserSetting(alice.ID, keyLLMAPIKey); !ok || v != "alice-key" {
		t.Fatalf("user setting = %q, %v", v, ok)
	}
	if v, ok, _ := s.st.GetSetting(keyLLMAPIKey); ok {
		t.Fatalf("global setting should be untouched, got %q", v)
	}

	// And reads it back with a keySet flag.
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: aliceCookie})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get settings = %d", rr.Code)
	}
	var settings struct {
		LLM struct {
			APIKeySet bool `json:"apiKeySet"`
		} `json:"llm"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.LLM.APIKeySet {
		t.Fatal("alice should see her key as set")
	}
}

func TestFirstSignupIsAdmin(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{DataDir: t.TempDir(), AllowSignup: true}, st)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/signup",
		strings.NewReader(`{"username":"first","password":"pw"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("first signup = %d, body %s", rr.Code, rr.Body.String())
	}
	u, err := s.st.GetUserByUsername("first")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin {
		t.Fatal("the first account must be an admin")
	}
	// A second signup is a plain user.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/signup",
		strings.NewReader(`{"username":"second","password":"pw"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("second signup = %d", rr.Code)
	}
	u2, err := s.st.GetUserByUsername("second")
	if err != nil {
		t.Fatal(err)
	}
	if u2.IsAdmin {
		t.Fatal("later signups should be plain users")
	}
}

func TestCreateProjectLLMNamed(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"My Cool App"}}]}`))
	}))
	defer llmSrv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{
		DataDir:      t.TempDir(),
		AuthDisabled: true,
		OpenAIBase:   llmSrv.URL,
		OpenAIKey:    "k",
		Model:        "m",
	}, st)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects",
		strings.NewReader(`{"description":"a to-do app with dark mode"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, body %s", rr.Code, rr.Body.String())
	}
	var created struct{ Name string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "My Cool App" {
		t.Fatalf("name = %q, want the LLM's name", created.Name)
	}
}

func TestCreateProjectLLMNameFallback(t *testing.T) {
	// A dead LLM endpoint falls back to the description's first line.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{
		DataDir:      t.TempDir(),
		AuthDisabled: true,
		OpenAIBase:   "http://127.0.0.1:1",
		OpenAIKey:    "k",
		Model:        "m",
	}, st)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects",
		strings.NewReader(`{"description":"a to-do app with dark mode"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, body %s", rr.Code, rr.Body.String())
	}
	var created struct{ Name string }
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "a to-do app with dark mode" {
		t.Fatalf("fallback name = %q", created.Name)
	}
}

func TestChatErrorPersisted(t *testing.T) {
	// A failing LLM endpoint: the turn errors and the failure must be
	// persisted so returning viewers see it (the SSE stream is gone by then).
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded."}}`))
	}))
	defer llmSrv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{
		DataDir:      t.TempDir(),
		AuthDisabled: true,
		OpenAIBase:   llmSrv.URL,
		OpenAIKey:    "k",
		Model:        "m",
	}, st)
	p := &store.Project{ID: store.NewID(), Name: "err", Path: t.TempDir()}
	if err := s.st.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects/"+p.ID+"/chat",
		strings.NewReader(`{"message":"hello"}`)))
	if !strings.Contains(rr.Body.String(), `"type":"error"`) {
		t.Fatalf("SSE stream lacks an error event: %s", rr.Body.String())
	}

	session, _ := s.st.EnsureDefaultSession(p.ID)
	msgs, err := s.st.ListMessages(p.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected user + error rows, got %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != "error" {
		t.Fatalf("last row role = %q, want error", last.Role)
	}
	if !strings.Contains(last.Content, "Rate limit exceeded") {
		t.Fatalf("error content = %q", last.Content)
	}
}

func TestAuthEndpoints(t *testing.T) {
	s, _, _ := newAuthServer(t)
	// Setup is no longer required — the first user exists.
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/setup", strings.NewReader(`{"username":"x","password":"y"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("setup after users exist = %d, want 400", rr.Code)
	}

	// Signup is disabled by default.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/signup", strings.NewReader(`{"username":"x","password":"y"}`)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("signup when disabled = %d, want 403", rr.Code)
	}

	// Login with the created account works and sets a session.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"pw"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("login = %d, body %s", rr.Code, rr.Body.String())
	}
	if len(rr.Result().Cookies()) != 1 {
		t.Fatalf("login should set a session cookie, got %d", len(rr.Result().Cookies()))
	}

	// Bad credentials → 401.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rr.Code)
	}
}
