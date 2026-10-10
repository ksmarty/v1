// Package extensions manages the JavaScript extensions the agent can load.
//
// An extension is a module at <root>/<id>/index.js whose default export is a
// factory called with pi-durable's extension API. It can contribute tools,
// prompt sections and hooks. Go never executes one: it validates and stores the
// source and asks the sidecar to reload, so a bad file is rejected here rather
// than at load time.
package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// idPattern keeps an id usable as a directory name: it is the only thing
// standing between a caller and a path outside the extensions root.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const (
	// maxSource bounds what one extension may be, so a runaway write cannot
	// fill the data volume.
	maxSource = 1 << 20
	// checkTimeout bounds the syntax check; it parses one file.
	checkTimeout = 20 * time.Second
)

// Extension is the metadata v1 keeps about one extension. The source lives on
// disk; this is what the settings UI and the sidecar's load list are built from.
type Extension struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Builtin     bool      `json:"builtin,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Root is the directory holding every extension, under the data volume so it
// survives a container rebuild.
func Root(dataDir string) string {
	return filepath.Join(dataDir, "extensions")
}

// Dir is one extension's directory.
func Dir(root, id string) string {
	return filepath.Join(root, id)
}

// SourcePath is the file the sidecar imports.
func SourcePath(root, id string) string {
	return filepath.Join(root, id, "index.js")
}

// Validate reports whether id and code are acceptable, checking the source with
// the parser that will actually run it.
//
// The check shells out to `node --check` rather than approximating JavaScript
// syntax in Go: the sidecar executes this file, so its parser is the authority,
// and a Go-side check would accept what Node rejects and reject what it accepts.
// node is guaranteed present because the sidecar itself is node.
func Validate(id, code string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid extension id %q: use lowercase letters, digits and dashes", id)
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("the extension source is empty")
	}
	if len(code) > maxSource {
		return fmt.Errorf("the extension source is too large (%d bytes, limit %d)", len(code), maxSource)
	}

	dir, err := os.MkdirTemp("", "v1-extension-check-")
	if err != nil {
		return fmt.Errorf("cannot create a temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)
	// A .mjs file so Node parses it as a module without depending on syntax
	// detection: an extension is ESM by definition.
	path := filepath.Join(dir, "index.mjs")
	if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
		return fmt.Errorf("cannot write the temporary file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--check", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

// Write stores an extension's source, replacing any previous file atomically so
// a reader never sees a half-written module.
func Write(root, id, code string) error {
	dir := Dir(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "index.js.tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(code); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), SourcePath(root, id))
}

// Read returns an extension's source. A missing file is not an error worth
// distinguishing: the caller shows an empty editor.
func Read(root, id string) (string, error) {
	data, err := os.ReadFile(SourcePath(root, id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// Remove deletes an extension's directory.
func Remove(root, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid extension id %q", id)
	}
	err := os.RemoveAll(Dir(root, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns the ids of the extensions present on disk.
func List(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

// Reconcile makes the metadata agree with what is on disk.
//
// A directory with no metadata still becomes an extension (so one dropped in by
// hand shows up rather than being invisible), and metadata whose directory is
// gone is dropped. Enabled defaults to false for anything discovered, because
// loading code nobody asked for is the wrong default.
func Reconcile(root string, known []Extension) []Extension {
	onDisk, err := List(root)
	if err != nil {
		return known
	}
	present := make(map[string]bool, len(onDisk))
	for _, id := range onDisk {
		present[id] = true
	}
	byID := make(map[string]Extension, len(known))
	for _, ext := range known {
		byID[ext.ID] = ext
	}

	out := make([]Extension, 0, len(onDisk))
	for _, id := range onDisk {
		if ext, ok := byID[id]; ok {
			out = append(out, ext)
			continue
		}
		out = append(out, Extension{ID: id, Name: id, CreatedAt: time.Now().UTC()})
	}
	return out
}

// Builtin is an extension v1 ships itself. The source is materialized into the
// extensions root like any other, so it can be read and edited in the UI.
type Builtin struct {
	Extension Extension
	Source    string
}

// FindBuiltin returns the builtin with the given id, or nil.
func FindBuiltin(id string) *Builtin {
	for _, builtin := range Builtins() {
		if builtin.Extension.ID == id {
			return &builtin
		}
	}
	return nil
}

// builtinManifest records the hash of each builtin's last shipped source, so a
// later startup can tell an untouched copy from one the user edited.
const builtinManifest = ".v1-builtins.json"

// Materialize installs the builtins v1 ships, upgrading a copy the user has not
// touched so a shipped fix reaches an existing install.
//
// A builtin the user edited is left alone: the shipped source is only rewritten
// when the on-disk file still matches the hash recorded the last time v1 wrote
// it. Deleting the file is therefore how you get the original back. A copy that
// predates the manifest has no recorded hash, so it is upgraded once (keeping
// the old source beside it as index.js.bak) rather than left stale forever.
func Materialize(root string, enabled map[string]bool) error {
	manifest := loadBuiltinManifest(root)
	dirty := false
	for _, builtin := range Builtins() {
		id := builtin.Extension.ID
		want := sourceHash(builtin.Source)
		onDisk, err := Read(root, id)
		if err != nil {
			return fmt.Errorf("cannot read the %s extension: %w", id, err)
		}
		recorded, tracked := manifest[id]
		if onDisk != "" && tracked && recorded != sourceHash(onDisk) {
			// The user edited it; leave it alone.
			continue
		}
		if onDisk != "" && !tracked && onDisk != builtin.Source {
			// Written before v1 tracked builtin hashes. Keep the old copy next
			// to the file so an edit made before tracking began is not lost.
			_ = os.WriteFile(SourcePath(root, id)+".bak", []byte(onDisk), 0o644)
		}
		if onDisk == builtin.Source {
			if recorded != want {
				manifest[id] = want
				dirty = true
			}
			continue
		}
		if err := Write(root, id, builtin.Source); err != nil {
			return fmt.Errorf("cannot install the %s extension: %w", id, err)
		}
		manifest[id] = want
		dirty = true
	}
	if dirty {
		return saveBuiltinManifest(root, manifest)
	}
	return nil
}

func loadBuiltinManifest(root string) map[string]string {
	data, err := os.ReadFile(filepath.Join(root, builtinManifest))
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]string{}
	}
	return out
}

func saveBuiltinManifest(root string, manifest map[string]string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, builtinManifest), data, 0o644)
}

func sourceHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
