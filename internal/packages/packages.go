// Package packages discovers pi packages on the npm registry and installs the
// part of them an agent can use.
//
// A pi package is an npm package tagged with the "pi-package" keyword. Many of
// them bundle agent skills alongside extension code. The extension code is
// written against the Pi CLI runtime, which v1 does not embed — v1's agents run
// on pi-durable — so it cannot execute here. The skills can, so installing a
// package imports them into the local skills directory.
package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// keyword marks an npm package as a pi package.
	keyword = "pi-package"
	// maxTarball caps a download so a bad response cannot exhaust memory.
	maxTarball = 64 << 20
	// maxMeta caps a registry metadata response.
	maxMeta = 4 << 20
	// maxSkillFile caps a single file inside a package.
	maxSkillFile = 4 << 20
	// maxSearch caps how many results one search may ask for.
	maxSearch = 50
)

// RegistryBase is the npm registry. It is a variable so tests can point the
// package at a local server.
var RegistryBase = "https://registry.npmjs.org"

// Package is a pi package as the npm registry describes it.
type Package struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Homepage    string   `json:"homepage"`
	Repository  string   `json:"repository"`
	NPMURL      string   `json:"npmUrl"`
	Keywords    []string `json:"keywords"`
	Downloads   int64    `json:"downloads"`
	Updated     string   `json:"updated"`
	// Skills names the skill directories bundled in the tarball. Search leaves
	// it empty; only Fetch and Install know.
	Skills []string `json:"skills,omitempty"`
}

// Skill is one skill bundled inside a package.
type Skill struct {
	// Dir is the skill's directory under the package's skills/ folder. It
	// becomes the directory name under the local skills root.
	Dir string `json:"dir"`
	// Name and Description come from the SKILL.md frontmatter, falling back to
	// the directory name.
	Name        string `json:"name"`
	Description string `json:"description"`
	// SkillMD is the raw SKILL.md so the UI can preview a skill before it is
	// installed.
	SkillMD string `json:"skillMd"`
	// Files holds every file in the skill directory, keyed by its path relative
	// to that directory.
	Files map[string][]byte `json:"-"`
}

// Search queries the npm registry for packages tagged as pi packages. An empty
// query lists the keyword's packages in the registry's own ranking.
func Search(ctx context.Context, q string, limit int) ([]Package, error) {
	if limit <= 0 || limit > maxSearch {
		limit = 20
	}
	text := "keywords:" + keyword
	if q = strings.TrimSpace(q); q != "" {
		text += " " + q
	}
	endpoint := RegistryBase + "/-/v1/search?size=" + strconv.Itoa(limit) +
		"&text=" + url.QueryEscape(text)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search the npm registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm registry error (HTTP %d)", resp.StatusCode)
	}
	var parsed npmSearch
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMeta)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("read the npm registry response: %w", err)
	}

	out := make([]Package, 0, len(parsed.Objects))
	for _, o := range parsed.Objects {
		p := Package{
			Name:        o.Package.Name,
			Version:     o.Package.Version,
			Description: o.Package.Description,
			Author:      authorName(o.Package.Author),
			Keywords:    o.Package.Keywords,
			Downloads:   o.Downloads.Monthly,
			Updated:     firstNonEmpty(o.Updated, o.Package.Date),
			NPMURL:      o.Package.Links.NPM,
			Homepage:    o.Package.Links.Homepage,
			Repository:  o.Package.Links.Repository,
		}
		if p.Name == "" {
			continue
		}
		// The registry treats "keywords:" as a text match, so a package whose
		// description merely mentions the phrase can come back. Filter those
		// out, but keep a result whose keyword list the registry omitted.
		if len(p.Keywords) > 0 && !hasKeyword(p.Keywords, keyword) {
			continue
		}
		if p.NPMURL == "" {
			p.NPMURL = "https://www.npmjs.com/package/" + p.Name
		}
		out = append(out, p)
	}
	return out, nil
}

// Fetch downloads a package's tarball and returns the skills it bundles. A
// package that bundles no skills yields an empty slice, not an error.
func Fetch(ctx context.Context, name, version string) ([]Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a package name is required")
	}
	if strings.TrimSpace(version) == "" {
		v, err := LatestVersion(ctx, name)
		if err != nil {
			return nil, err
		}
		version = v
	}
	body, err := download(ctx, tarballURL(name, version))
	if err != nil {
		return nil, err
	}
	return extractSkills(body)
}

// Write writes the skill's files into root/<dir>.
func (s Skill) Write(root string) error {
	if s.Dir == "" || s.Dir != filepath.Base(s.Dir) || s.Dir == "." || s.Dir == ".." {
		return fmt.Errorf("unsafe skill directory %q", s.Dir)
	}
	base := filepath.Join(root, s.Dir)
	for rel, data := range s.Files {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("unsafe skill file %q", rel)
		}
		full := filepath.Join(base, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// tarballURL is where the registry serves a version's tarball. A scoped
// package uses its bare name in the file name.
func tarballURL(name, version string) string {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	return RegistryBase + "/" + name + "/-/" + base + "-" + version + ".tgz"
}

// LatestVersion resolves the version the registry publishes as latest.
func LatestVersion(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a package name is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RegistryBase+"/"+name, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("look up %s (HTTP %d)", name, resp.StatusCode)
	}
	var meta struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMeta)).Decode(&meta); err != nil {
		return "", fmt.Errorf("read the registry response for %s: %w", name, err)
	}
	if v := meta.DistTags["latest"]; v != "" {
		return v, nil
	}
	return "", fmt.Errorf("package %s has no published version", name)
}

func download(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download the package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download the package (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTarball+1))
	if err != nil {
		return nil, fmt.Errorf("download the package: %w", err)
	}
	if len(body) > maxTarball {
		return nil, fmt.Errorf("the package tarball is larger than %d bytes", maxTarball)
	}
	return body, nil
}

// extractSkills reads a gzipped npm tarball and collects the skill directories
// under its skills/ folder. npm wraps a package's contents in a package/ prefix.
func extractSkills(tarball []byte) ([]Skill, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("read the package tarball: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	byDir := map[string]*Skill{}
	var order []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the package tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel, ok := skillPath(hdr.Name)
		if !ok {
			continue
		}
		// rel is relative to skills/, so its first segment names the skill and
		// the rest is the path within it.
		dir, file, found := strings.Cut(rel, "/")
		if !found || dir == "" || file == "" {
			continue
		}
		s := byDir[dir]
		if s == nil {
			s = &Skill{Dir: dir, Files: map[string][]byte{}}
			byDir[dir] = s
			order = append(order, dir)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxSkillFile))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		s.Files[file] = data
	}

	out := make([]Skill, 0, len(order))
	for _, dir := range order {
		s := byDir[dir]
		md, ok := s.Files["SKILL.md"]
		if !ok {
			// A directory under skills/ with no SKILL.md is not a skill.
			continue
		}
		s.SkillMD = string(md)
		s.Name, s.Description = frontmatter(s.SkillMD)
		if s.Name == "" {
			s.Name = dir
		}
		out = append(out, *s)
	}
	return out, nil
}

// skillPath returns a path relative to the package's skills/ directory, or
// false when the entry is not a file inside one.
func skillPath(name string) (string, bool) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	clean = strings.TrimPrefix(clean, "package/")
	if clean == "" || clean == "package" || clean == "." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	rest, ok := strings.CutPrefix(clean, "skills/")
	if !ok || rest == "" || strings.Contains(rest, "..") {
		return "", false
	}
	if rest != path.Clean(rest) {
		return "", false
	}
	return rest, true
}

// frontmatter reads name and description from a SKILL.md frontmatter block.
// It is deliberately minimal: skills only ever use flat scalar values.
func frontmatter(md string) (name, description string) {
	block, ok := frontmatterBlock(md)
	if !ok {
		return "", ""
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			if name == "" {
				name = value
			}
		case "description":
			if description == "" {
				description = value
			}
		}
	}
	return name, description
}

func frontmatterBlock(md string) (string, bool) {
	md = strings.TrimLeft(md, "\ufeff \t\r\n")
	if !strings.HasPrefix(md, "---") {
		return "", false
	}
	rest := strings.TrimPrefix(md, "---")
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// authorName reads the author field, which npm serves either as a string or as
// an object carrying a name.
func authorName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Name
	}
	return ""
}

func hasKeyword(keywords []string, want string) bool {
	for _, k := range keywords {
		if strings.EqualFold(k, want) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// npmSearch is the shape of the registry's search response, reduced to the
// fields the UI shows.
type npmSearch struct {
	Objects []struct {
		Package struct {
			Name        string          `json:"name"`
			Version     string          `json:"version"`
			Description string          `json:"description"`
			Keywords    []string        `json:"keywords"`
			Date        string          `json:"date"`
			Author      json.RawMessage `json:"author"`
			Links       struct {
				NPM        string `json:"npm"`
				Homepage   string `json:"homepage"`
				Repository string `json:"repository"`
			} `json:"links"`
		} `json:"package"`
		Downloads struct {
			Monthly int64 `json:"monthly"`
		} `json:"downloads"`
		Updated string `json:"updated"`
	} `json:"objects"`
}
