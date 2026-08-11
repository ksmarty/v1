package server

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"v1/internal/store"
)

// User management — admin only. Users see strictly their own projects, so
// deleting a user cascades: previews stop, terminals die, project directories
// are removed and the database rows go with them.

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.st.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id":        u.ID,
			"username":  u.Username,
			"isAdmin":   u.IsAdmin,
			"createdAt": u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	if msg := validateUsername(username); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password must not be empty")
		return
	}
	// The first account of an instance is always an admin, whoever creates it.
	isAdmin := body.IsAdmin
	if n, err := s.st.UserCount(); err == nil && n == 0 {
		isAdmin = true
	}
	u, err := s.auth.CreateUser(username, body.Password, isAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        u.ID,
		"username":  u.Username,
		"isAdmin":   u.IsAdmin,
		"createdAt": u.CreatedAt,
	})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	me := s.currentUser(r)
	targetID := r.PathValue("id")
	target, err := s.st.GetUserByID(targetID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Password *string `json:"password"`
		IsAdmin  *bool   `json:"isAdmin"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Password != nil && *body.Password != "" {
		if len(*body.Password) < 4 {
			writeError(w, http.StatusBadRequest, "password must be at least 4 characters")
			return
		}
		if err := s.auth.SetPassword(target.ID, *body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.IsAdmin != nil {
		if target.ID == me.ID && !*body.IsAdmin {
			writeError(w, http.StatusBadRequest, "cannot remove your own admin role")
			return
		}
		if target.IsAdmin && !*body.IsAdmin {
			if n, err := s.st.AdminCount(); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			} else if n <= 1 {
				writeError(w, http.StatusBadRequest, "cannot demote the last admin")
				return
			}
		}
		if err := s.st.SetUserAdmin(target.ID, *body.IsAdmin); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	updated, err := s.st.GetUserByID(target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        updated.ID,
		"username":  updated.Username,
		"isAdmin":   updated.IsAdmin,
		"createdAt": updated.CreatedAt,
	})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	me := s.currentUser(r)
	targetID := r.PathValue("id")
	if targetID == me.ID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if _, err := s.st.GetUserByID(targetID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Tear down the user's projects before removing the rows.
	projects, err := s.st.ListProjectsByOwner(targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range projects {
		s.previews.Stop(p.ID)
		s.terminals.KillProject(p.ID)
	}
	if err := s.st.DeleteUser(targetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range projects {
		_ = os.RemoveAll(p.Path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "projects": len(projects)})
}
