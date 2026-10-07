package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"v1/internal/extensions"
)

// extensionsRoot is the directory extensions live in.
func (s *Server) extensionsRoot() string {
	return extensions.Root(s.cfg.DataDir)
}

// storedExtensions returns the persisted metadata exactly as saved, without
// reconciling it against disk. Callers that need to tell "no entry yet" apart
// from "an entry the user changed" must use this.
func (s *Server) storedExtensions() []extensions.Extension {
	var known []extensions.Extension
	if v, ok, _ := s.st.GetSetting(keyExtensions); ok && v != "" {
		_ = json.Unmarshal([]byte(v), &known)
	}
	return known
}

// installedExtensions returns the persisted metadata, reconciled with what is
// actually on disk so a directory added or deleted by hand is reflected.
func (s *Server) installedExtensions() []extensions.Extension {
	return extensions.Reconcile(s.extensionsRoot(), s.storedExtensions())
}

func (s *Server) saveExtensions(list []extensions.Extension) error {
	if list == nil {
		list = []extensions.Extension{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.st.SetSetting(keyExtensions, string(raw))
}

// ensureBuiltinExtensions writes the extensions v1 ships and registers them,
// enabled, so a fresh install has a working sub-agent tool.
//
// It runs at startup: a bundled extension whose file is missing is restored
// (which is also how a user gets the original back after editing it), while one
// that is present is left exactly as it is.
func (s *Server) ensureBuiltinExtensions() {
	if err := extensions.Materialize(s.extensionsRoot(), nil); err != nil {
		log.Printf("extensions: %v", err)
	}
	// Merge into the stored list rather than the reconciled one: a builtin with
	// no stored entry must keep its own default (enabled), while one that has an
	// entry keeps whatever the user chose.
	list := s.storedExtensions()
	changed := false
	for _, builtin := range extensions.Builtins() {
		found := false
		for i := range list {
			if list[i].ID != builtin.Extension.ID {
				continue
			}
			if !list[i].Builtin {
				list[i].Builtin = true
				changed = true
			}
			found = true
			break
		}
		if !found {
			list = append(list, builtin.Extension)
			changed = true
		}
	}
	if changed {
		if err := s.saveExtensions(list); err != nil {
			log.Printf("extensions: cannot persist the bundled extensions: %v", err)
		}
	}
}

// extensionLoadState asks the sidecar what it has loaded. It is best effort:
// extensions work with the harness stopped, so the settings page must still
// render, and a stopped harness is reported rather than treated as an error.
func (s *Server) extensionLoadState(ctx context.Context) map[string]any {
	harness := s.harnessBridge()
	if harness == nil {
		return map[string]any{"available": false, "reason": "the agent harness is not running"}
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := harness.ExtensionsList(callCtx)
	if err != nil {
		return map[string]any{"available": false, "reason": err.Error()}
	}
	result["available"] = true
	return result
}

// reloadExtensions asks the sidecar to re-read the extensions directory so a
// change takes effect on the next turn without restarting the harness. The
// enabled set is pushed with it, because a disabled extension must never be
// loaded — filtering afterwards would still have run its code.
func (s *Server) reloadExtensions(ctx context.Context) map[string]any {
	harness := s.harnessBridge()
	if harness == nil {
		return map[string]any{"reloaded": false, "reason": "the agent harness is not running"}
	}
	enabled := []string{}
	for _, ext := range s.installedExtensions() {
		if ext.Enabled {
			enabled = append(enabled, ext.ID)
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := harness.ExtensionsReload(callCtx, enabled)
	if err != nil {
		return map[string]any{"reloaded": false, "reason": err.Error()}
	}
	return map[string]any{"reloaded": true, "state": result}
}

// createExtension installs an extension the agent wrote: it validates and
// writes the source, records it as enabled, and asks the sidecar to reload so
// it takes effect on the next turn. It backs the create_extension tool.
//
// The result names the tools and prompt sections the extension contributed, or
// returns the load error so the agent can fix it.
func (s *Server) createExtension(ctx context.Context, id, description, source string) (string, error) {
	if err := extensions.Validate(id, source); err != nil {
		return "", err
	}
	if err := extensions.Write(s.extensionsRoot(), id, source); err != nil {
		return "", err
	}

	list := s.installedExtensions()
	now := time.Now().UTC()
	updated := false
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if description != "" {
			list[i].Description = description
		}
		list[i].Enabled = true
		list[i].UpdatedAt = now
		updated = true
		break
	}
	if !updated {
		list = append(list, extensions.Extension{
			ID:          id,
			Name:        id,
			Description: description,
			Enabled:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	if err := s.saveExtensions(list); err != nil {
		return "", err
	}

	reload := s.reloadExtensions(ctx)
	outcome := extensionReloadOutcome(reload, id)
	if outcome.err != "" {
		return "", fmt.Errorf("extension %q was written but the harness could not load it: %s", id, outcome.err)
	}
	message := fmt.Sprintf("installed extension %q and enabled it", id)
	if len(outcome.tools) > 0 {
		message += "; tools: " + strings.Join(outcome.tools, ", ")
	}
	if len(outcome.sections) > 0 {
		message += "; sections: " + strings.Join(outcome.sections, ", ")
	}
	if !outcome.reloaded {
		reason, _ := reload["reason"].(string)
		if reason == "" {
			reason = "the agent harness is not running"
		}
		message += " (the harness is not running, so it will load on the next start: " + reason + ")"
	}
	return message, nil
}

// extensionOutcome describes one extension in a reload response, so the
// create_extension tool can report what the extension contributed.
type extensionOutcome struct {
	reloaded bool
	tools    []string
	sections []string
	err      string
}

func extensionReloadOutcome(reload map[string]any, id string) extensionOutcome {
	var out extensionOutcome
	out.reloaded, _ = reload["reloaded"].(bool)
	state, _ := reload["state"].(map[string]any)
	if state == nil {
		return out
	}
	if loadErrors, ok := state["errors"].([]any); ok {
		for _, item := range loadErrors {
			entry, _ := item.(map[string]any)
			if entry == nil {
				continue
			}
			if entryID, _ := entry["id"].(string); entryID == id {
				out.err, _ = entry["message"].(string)
			}
		}
	}
	if loaded, ok := state["loaded"].([]any); ok {
		for _, item := range loaded {
			entry, _ := item.(map[string]any)
			if entry == nil {
				continue
			}
			if entryID, _ := entry["id"].(string); entryID != id {
				continue
			}
			out.tools = anyStrings(entry["tools"])
			out.sections = anyStrings(entry["sections"])
		}
	}
	return out
}

func anyStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// extensionLoadErrors flattens the sidecar's per-extension load failures into
// the plain list the settings UI displays.
func extensionLoadErrors(state map[string]any) []string {
	raw, ok := state["errors"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		message, _ := entry["message"].(string)
		if id == "" {
			out = append(out, message)
			continue
		}
		out = append(out, id+": "+message)
	}
	return out
}

func (s *Server) handleExtensionsList(w http.ResponseWriter, r *http.Request) {
	state := s.extensionLoadState(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": s.installedExtensions(),
		"harness":    state,
		"errors":     extensionLoadErrors(state),
	})
}

// handleExtensionGet returns one extension including its source, so the editor
// can show what is actually on disk rather than what was last submitted.
func (s *Server) handleExtensionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list := s.installedExtensions()
	var found *extensions.Extension
	for i := range list {
		if list[i].ID == id {
			found = &list[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "extension not found")
		return
	}
	source, err := extensions.Read(s.extensionsRoot(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          found.ID,
		"name":        found.Name,
		"description": found.Description,
		"enabled":     found.Enabled,
		"builtin":     found.Builtin,
		"source":      source,
	})
}

// handleExtensionSave creates or updates an extension.
func (s *Server) handleExtensionSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
		Enabled     *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// The source is checked with node before it is stored: a syntax error is
	// reported here, where it can be fixed, instead of surfacing as a load
	// failure on the next turn.
	if err := extensions.Validate(body.ID, body.Source); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := extensions.Write(s.extensionsRoot(), body.ID, body.Source); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	list := s.installedExtensions()
	now := time.Now().UTC()
	updated := false
	for i := range list {
		if list[i].ID != body.ID {
			continue
		}
		if body.Name != "" {
			list[i].Name = body.Name
		}
		if body.Description != "" {
			list[i].Description = body.Description
		}
		if body.Enabled != nil {
			list[i].Enabled = *body.Enabled
		}
		list[i].UpdatedAt = now
		updated = true
		break
	}
	if !updated {
		name := body.Name
		if name == "" {
			name = body.ID
		}
		// A new extension starts disabled unless the caller says otherwise:
		// loading code nobody asked for is the wrong default.
		enabled := false
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		list = append(list, extensions.Extension{
			ID:          body.ID,
			Name:        name,
			Description: body.Description,
			Enabled:     enabled,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	if err := s.saveExtensions(list); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": s.installedExtensions(),
		"reload":     s.reloadExtensions(r.Context()),
	})
}

// handleExtensionsToggle enables or disables an extension.
func (s *Server) handleExtensionsToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	list := s.installedExtensions()
	for i := range list {
		if list[i].ID != body.ID {
			continue
		}
		if body.Enabled != nil {
			list[i].Enabled = *body.Enabled
		} else {
			list[i].Enabled = !list[i].Enabled
		}
		list[i].UpdatedAt = time.Now().UTC()
		if err := s.saveExtensions(list); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"extensions": s.installedExtensions(),
			"reload":     s.reloadExtensions(r.Context()),
		})
		return
	}
	writeError(w, http.StatusNotFound, "extension not found")
}

// handleExtensionsRemove deletes an extension and its source.
func (s *Server) handleExtensionsRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// A bundled extension ships with v1, so deleting it would only bring it
	// back on the next start. Disabling is the supported way to turn it off.
	if extensions.FindBuiltin(body.ID) != nil {
		writeError(w, http.StatusBadRequest, "bundled extensions can be disabled but not removed")
		return
	}
	if err := extensions.Remove(s.extensionsRoot(), body.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := s.installedExtensions()
	out := make([]extensions.Extension, 0, len(list))
	for _, ext := range list {
		if ext.ID == body.ID {
			continue
		}
		out = append(out, ext)
	}
	if err := s.saveExtensions(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": s.installedExtensions(),
		"reload":     s.reloadExtensions(r.Context()),
	})
}

// handleExtensionsReload re-reads the extensions directory in the sidecar.
func (s *Server) handleExtensionsReload(w http.ResponseWriter, r *http.Request) {
	state := s.extensionLoadState(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"extensions": s.installedExtensions(),
		"harness":    state,
		"errors":     extensionLoadErrors(state),
		"reload":     s.reloadExtensions(r.Context()),
	})
}
