// Package auth implements user login, sessions and the auth middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"v1/internal/store"
)

// CookieName is the session cookie name.
const CookieName = "v1_session"

const sessionTTL = 30 * 24 * time.Hour

// userCtxKey carries the authenticated *store.User through the middleware.
type userCtxKey struct{}

// Manager handles authentication state.
type Manager struct {
	st          *store.Store
	disabled    bool
	envPass     string
	oidcEnabled bool
}

// NewManager creates a Manager. When disabled is true all requests pass.
func NewManager(st *store.Store, disabled bool, envPassword string) *Manager {
	return &Manager{st: st, disabled: disabled, envPass: envPassword}
}

// SetOIDCEnabled marks OIDC (e.g. Authentik) login as configured; when
// enabled, the first-run password setup is not required.
func (m *Manager) SetOIDCEnabled(enabled bool) {
	m.oidcEnabled = enabled
}

// HashPassword bcrypt-hashes a plaintext password.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// BootstrapAdmin creates the admin user from the env-provided password on
// first start (when no users exist yet). Setup remains required otherwise.
func (m *Manager) BootstrapAdmin() {
	if m.envPass == "" {
		return
	}
	if n, err := m.st.UserCount(); err != nil || n > 0 {
		return
	}
	h, err := HashPassword(m.envPass)
	if err != nil {
		return
	}
	u := &store.User{
		ID:           store.NewID(),
		Username:     "admin",
		PasswordHash: h,
		IsAdmin:      true,
	}
	if err := m.st.CreateUser(u); err != nil {
		return
	}
	_ = m.st.ClaimOwnerlessProjects(u.ID)
}

// SetupRequired reports whether no user account exists anywhere yet.
func (m *Manager) SetupRequired() bool {
	if m.disabled {
		return false
	}
	if m.oidcEnabled {
		return false
	}
	n, err := m.st.UserCount()
	return err == nil && n == 0
}

// Login verifies a username/password pair and returns the user.
func (m *Manager) Login(username, password string) (*store.User, bool) {
	u, err := m.st.GetUserByUsername(username)
	if err != nil {
		return nil, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, false
	}
	return u, true
}

// SetPassword stores a new bcrypt password hash for a user.
func (m *Manager) SetPassword(userID, password string) error {
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	return m.st.SetUserPassword(userID, h)
}

// CreateUser creates a user with a bcrypt-hashed password.
func (m *Manager) CreateUser(username, password string, isAdmin bool) (*store.User, error) {
	h, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &store.User{
		ID:           store.NewID(),
		Username:     username,
		PasswordHash: h,
		IsAdmin:      isAdmin,
	}
	if err := m.st.CreateUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// CreateSession stores a session for the user and sets the session cookie.
func (m *Manager) CreateSession(w http.ResponseWriter, r *http.Request, userID string) error {
	token := newToken()
	if err := m.st.CreateSession(token, userID, sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
		Expires:  time.Now().Add(sessionTTL),
	})
	return nil
}

// DestroySession deletes the session (if any) and clears the cookie.
func (m *Manager) DestroySession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = m.st.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
		MaxAge:   -1,
	})
}

// User resolves the authenticated user from the request, or (nil, false).
func (m *Manager) User(r *http.Request) (*store.User, bool) {
	if m.disabled {
		u := &store.User{ID: "local", Username: "local", IsAdmin: true}
		return u, true
	}
	if u, ok := UserFromContext(r.Context()); ok {
		return u, true
	}
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	userID, ok, err := m.st.SessionUser(c.Value)
	if err != nil || !ok {
		return nil, false
	}
	u, err := m.st.GetUserByID(userID)
	if err != nil {
		return nil, false
	}
	return u, true
}

// UserFromContext returns the user the middleware attached to the request.
func UserFromContext(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(*store.User)
	return u, ok
}

// Authenticated reports whether the request carries a valid session.
func (m *Manager) Authenticated(r *http.Request) bool {
	_, ok := m.User(r)
	return ok
}

// Middleware protects everything except /api/auth/* and /api/healthz. The
// authenticated user is attached to the request context (the local dev user
// when auth is disabled).
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.disabled {
			u := &store.User{ID: "local", Username: "local", IsAdmin: true}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u)))
			return
		}
		if r.URL.Path == "/api/healthz" || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		u, ok := m.User(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u)))
	})
}
