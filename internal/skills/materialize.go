package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// builtinManifestFile records the hash of each bundled skill's files as v1 last
// wrote them, so a later startup can refresh an untouched copy and leave one
// the user edited alone.
const builtinManifestFile = ".v1-builtins.json"

// InstallBuiltin writes a bundled skill's files into root/<dir> and records
// their hash, so a later refresh can tell an untouched copy from an edited one.
func InstallBuiltin(root string, b Builtin) error {
	if err := materializeBuiltin(root, b); err != nil {
		return err
	}
	manifest := loadBuiltinManifest(root)
	manifest[b.Skill.ID] = hashBuiltinFiles(b.Files)
	return saveBuiltinManifest(root, manifest)
}

// RefreshBuiltins upgrades the bundled skills the user has not edited, so a
// shipped change to a SKILL.md reaches an existing install. Only skills named
// in installed are touched, so removing a bundled skill keeps it removed. The
// shipped files are rewritten only when the on-disk hash matches the hash
// recorded the last time v1 wrote it; a copy the user edited is left alone. A
// copy that predates the manifest is upgraded once, keeping the old files
// beside it as <file>.bak.
func RefreshBuiltins(root string, installed map[string]bool) error {
	manifest := loadBuiltinManifest(root)
	dirty := false
	for _, b := range Builtins() {
		if !installed[b.Skill.ID] {
			continue
		}
		want := hashBuiltinFiles(b.Files)
		disk, exists := readBuiltinFiles(root, b)
		recorded, tracked := manifest[b.Skill.ID]
		if exists && tracked && recorded != hashBuiltinFiles(disk) {
			// The user edited it; leave it alone.
			continue
		}
		if exists && !tracked && hashBuiltinFiles(disk) != want {
			// Written before v1 tracked builtin hashes. Keep the old copy beside
			// the file so an edit made before tracking began is not lost.
			backupBuiltin(root, b)
		}
		if exists && hashBuiltinFiles(disk) == want {
			if recorded != want {
				manifest[b.Skill.ID] = want
				dirty = true
			}
			continue
		}
		if err := materializeBuiltin(root, b); err != nil {
			return fmt.Errorf("cannot refresh the %s skill: %w", b.Skill.ID, err)
		}
		manifest[b.Skill.ID] = want
		dirty = true
	}
	if dirty {
		return saveBuiltinManifest(root, manifest)
	}
	return nil
}

// materializeBuiltin writes the bundled skill's files into root/<dir>,
// overwriting them in place. It deliberately does not clear the directory, so a
// .bak written before an upgrade survives and user files are never removed.
func materializeBuiltin(root string, b Builtin) error {
	dir := b.Skill.Dir
	if dir == "" || dir != filepath.Base(dir) || strings.ContainsAny(dir, `/\`) {
		return os.ErrInvalid
	}
	dest := filepath.Join(root, dir)
	for rel, content := range b.Files {
		full := filepath.Join(dest, filepath.FromSlash(rel))
		if !strings.HasPrefix(full, dest+string(filepath.Separator)) {
			return os.ErrInvalid
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// readBuiltinFiles reads the files a builtin would write, reporting whether any
// of them exist on disk.
func readBuiltinFiles(root string, b Builtin) (map[string]string, bool) {
	out := map[string]string{}
	exists := false
	for rel := range b.Files {
		data, err := os.ReadFile(filepath.Join(root, b.Skill.Dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		out[rel] = string(data)
		exists = true
	}
	return out, exists
}

// backupBuiltin copies a builtin's on-disk files to <file>.bak.
func backupBuiltin(root string, b Builtin) {
	for rel := range b.Files {
		full := filepath.Join(root, b.Skill.Dir, filepath.FromSlash(rel))
		if data, err := os.ReadFile(full); err == nil {
			_ = os.WriteFile(full+".bak", data, 0o644)
		}
	}
}

// hashBuiltinFiles hashes a skill's files deterministically (paths sorted), so
// a manifest comparison is stable.
func hashBuiltinFiles(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00", k, len(files[k]))
		h.Write([]byte(files[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func loadBuiltinManifest(root string) map[string]string {
	data, err := os.ReadFile(filepath.Join(root, builtinManifestFile))
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
	return os.WriteFile(filepath.Join(root, builtinManifestFile), data, 0o644)
}
