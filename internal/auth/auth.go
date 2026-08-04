// Package auth implements password login, sessions and the auth middleware.
package auth

import (
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

const passwordHashKey = "password_hash"

const sessionTTL = 30 * 24 * time.Hour

// Manager handles authentication state.
type Manager struct {
	st       *store.Store
	disabled bool
	envPass  string
}

// NewManager creates a Manager. When disabled is true all requests pass.
func NewManager(st *store.Store, disabled bool, envPassword string) *Manager {
	return &Manager{st: st, disabled: disabled, envPass: envPassword}
}

// HashPassword bcrypt-hashes a plaintext password.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// EnsureEnvPassword hashes the env-provided password into the settings table
// on first use (when no hash is stored yet).
func (m *Manager) EnsureEnvPassword() {
	if _, ok, _ := m.st.GetSetting(passwordHashKey); ok {
		return
	}
	if m.envPass == "" {
		return
	}
	if h, err := HashPassword(m.envPass); err == nil {
		_ = m.st.SetSetting(passwordHashKey, h)
	}
}

// SetupRequired reports whether no password exists anywhere yet.
func (m *Manager) SetupRequired() bool {
	if m.disabled {
		return false
	}
	_, ok, _ := m.st.GetSetting(passwordHashKey)
	return !ok && m.envPass == ""
}

// Verify checks a plaintext password against the stored bcrypt hash.
func (m *Manager) Verify(password string) bool {
	hash, ok, err := m.st.GetSetting(passwordHashKey)
	if err != nil || !ok || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// SetPassword stores a new bcrypt password hash.
func (m *Manager) SetPassword(password string) error {
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	return m.st.SetSetting(passwordHashKey, h)
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

// CreateSession stores a new session and sets the session cookie.
func (m *Manager) CreateSession(w http.ResponseWriter, r *http.Request) error {
	token := newToken()
	if err := m.st.CreateSession(token, sessionTTL); err != nil {
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

// Authenticated reports whether the request carries a valid session.
func (m *Manager) Authenticated(r *http.Request) bool {
	if m.disabled {
		return true
	}
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := m.st.SessionValid(c.Value)
	return err == nil && ok
}

// Middleware protects everything except /api/auth/* and /api/healthz.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.disabled || r.URL.Path == "/api/healthz" || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if !m.Authenticated(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
