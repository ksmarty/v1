// Package skills discovers agent skills on the SkillsMP marketplace
// (skillsmp.com) and installs them into the local data directory. A skill is a
// directory containing a SKILL.md (plus optional scripts/templates); installed
// skills are injected into the agent's system prompt.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"v1/internal/gitops"
)

const apiBase = "https://skillsmp.com"

// Skill is one skill on the marketplace (or installed locally).
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Description string `json:"description"`
	GitHubURL   string `json:"githubUrl"`
	Branch      string `json:"branch"`
	SourcePath  string `json:"sourcePath"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Dir         string `json:"dir"`
	Enabled     bool   `json:"enabled"`
}

// Search queries the SkillsMP index.
func Search(ctx context.Context, q string, limit int) ([]Skill, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/api/skills?search="+escape(q)+"&limit="+fmt.Sprint(limit), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skillsmp API error (HTTP %d)", resp.StatusCode)
	}
	var out struct {
		Skills []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Author      string `json:"author"`
			Description string `json:"description"`
			GitHubURL   string `json:"githubUrl"`
			Branch      string `json:"branch"`
			Route       struct {
				OwnerSlug        string `json:"ownerSlug"`
				RepoSlug         string `json:"repoSlug"`
				SourceSkillPath  string `json:"sourceSkillPath"`
			} `json:"route"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	skills := make([]Skill, 0, len(out.Skills))
	for _, s := range out.Skills {
		sk := Skill{
			ID:          s.ID,
			Name:        s.Name,
			Author:      s.Author,
			Description: s.Description,
			GitHubURL:   s.GitHubURL,
			Branch:      s.Branch,
			Owner:       s.Route.OwnerSlug,
			Repo:        s.Route.RepoSlug,
			SourcePath:  s.Route.SourceSkillPath,
		}
		if sk.Branch == "" {
			sk.Branch = "main"
		}
		// Fall back to parsing the GitHub URL when route metadata is missing.
		if sk.Owner == "" || sk.Repo == "" {
			owner, repo, rest := parseGitHubURL(s.GitHubURL)
			sk.Owner, sk.Repo = owner, repo
			if sk.SourcePath == "" && strings.Contains(rest, "/tree/") {
				parts := strings.SplitN(rest, "/tree/", 2)
				if len(parts) == 2 && parts[0] != "" {
					sk.Branch = parts[0]
					sk.SourcePath = strings.TrimPrefix(parts[1], "/")
				}
			}
		}
		if sk.SourcePath == "" {
			sk.SourcePath = "."
		}
		skills = append(skills, sk)
	}
	return skills, nil
}

// parseGitHubURL extracts owner/repo and the path tail from a github URL.
func parseGitHubURL(u string) (owner, repo, rest string) {
	u = strings.TrimPrefix(u, "https://github.com/")
	u = strings.TrimPrefix(u, "http://github.com/")
	parts := strings.SplitN(u, "/", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	return parts[0], parts[1], strings.TrimPrefix(parts[2], "/")
}

func escape(s string) string {
	r := strings.NewReplacer(" ", "+", "&", "%26", "=", "%3D", "#", "%23")
	return r.Replace(s)
}

// Install downloads the skill's directory from GitHub into root/<dir> and
// returns the record with Dir and a fresh id (the marketplace id may be too
// long for a directory name).
func Install(ctx context.Context, gh *gitops.GHClient, s Skill, root string) (Skill, error) {
	if s.Owner == "" || s.Repo == "" {
		return Skill{}, fmt.Errorf("skill source is missing owner/repo")
	}
	dir := sanitize(s.Name)
	if dir == "" {
		dir = sanitize(s.Owner + "-" + s.Repo)
	}
	if dir == "" {
		return Skill{}, fmt.Errorf("could not derive a directory name for the skill")
	}
	dest := filepath.Join(root, dir)
	if err := os.RemoveAll(dest); err != nil {
		return Skill{}, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Skill{}, err
	}

	httpc := &http.Client{Timeout: 30 * time.Second}
	treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1",
		s.Owner, s.Repo, s.Branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return Skill{}, err
	}
	req.Header.Set("User-Agent", "v1")
	if gh != nil && gh.Token != "" {
		req.Header.Set("Authorization", "Bearer "+gh.Token)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return Skill{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Skill{}, fmt.Errorf("GitHub API error (HTTP %d) listing %s/%s", resp.StatusCode, s.Owner, s.Repo)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&tree); err != nil {
		return Skill{}, err
	}

	prefix := strings.Trim(s.SourcePath, "/")
	prefixDir := prefix + "/"
	// Marketplace paths usually point at the SKILL.md file itself; detect that
	// so we download just that one file instead of treating it as a directory.
	isFile := false
	if prefix != "" && prefix != "." {
		for _, t := range tree.Tree {
			if t.Type == "blob" && t.Path == prefix {
				isFile = true
				break
			}
		}
	}
	count := 0
	for _, t := range tree.Tree {
		if t.Type != "blob" {
			continue
		}
		var rel string
		if isFile {
			if t.Path == prefix {
				rel = "SKILL.md"
			} else {
				continue
			}
		} else {
			switch {
			case prefix == "" || prefix == ".":
				rel = t.Path
			case t.Path == prefix:
				continue // the SKILL.md path itself may equal the prefix dir
			case strings.HasPrefix(t.Path, prefixDir):
				rel = strings.TrimPrefix(t.Path, prefixDir)
			default:
				continue
			}
		}
		if rel == "" {
			continue
		}
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
			s.Owner, s.Repo, s.Branch, t.Path)
		if err := download(ctx, httpc, rawURL, filepath.Join(dest, rel)); err != nil {
			return Skill{}, err
		}
		count++
	}
	if count == 0 {
		_ = os.RemoveAll(dest)
		return Skill{}, fmt.Errorf("no files found under %s in %s/%s", prefix, s.Owner, s.Repo)
	}
	s.ID = dir
	s.Dir = dir
	s.Enabled = true
	return s, nil
}

func download(ctx context.Context, c *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "v1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub raw error (HTTP %d) for %s", resp.StatusCode, url)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(resp.Body, 4<<20))
	return err
}

// SystemPrompt renders the SKILL.md contents of every enabled installed skill
// for injection into the agent's system prompt.
func SystemPrompt(root string, installed []Skill) string {
	var b strings.Builder
	for _, s := range installed {
		if !s.Enabled || s.Dir == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, s.Dir, "SKILL.md"))
		if err != nil || strings.TrimSpace(string(content)) == "" {
			continue
		}
		b.WriteString("\n---\n")
		b.WriteString("## Skill: ")
		b.WriteString(s.Name)
		if s.Description != "" {
			b.WriteString(" — ")
			b.WriteString(s.Description)
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(string(content)))
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "You have the following skills installed. Read their instructions and use them when the task matches.\n" + b.String()
}

// sanitize turns a name into a safe directory name.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(strings.TrimPrefix(b.String(), "."), "-")
	if out == "" {
		return "skill"
	}
	return out
}
