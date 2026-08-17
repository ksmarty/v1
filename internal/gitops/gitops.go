// Package gitops implements GitHub REST calls and local git operations
// (clone, status, commit, push) via go-git.
package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const apiBase = "https://api.github.com"

// GHClient is a minimal GitHub REST client.
type GHClient struct {
	Token string
	HTTP  *http.Client
}

// NewGHClient creates a client authenticated with the given token.
func NewGHClient(token string) *GHClient {
	return &GHClient{Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *GHClient) do(ctx context.Context, method, url string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "v1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

// Request performs an arbitrary GitHub REST request and returns the HTTP
// status, response body and any transport error. The token is applied when set
// (the client works anonymously for public endpoints otherwise).
func (c *GHClient) Request(ctx context.Context, method, url string, body any) (int, []byte, error) {
	return c.do(ctx, method, url, body)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Repo is a GitHub repository summary.
type Repo struct {
	Name      string `json:"name"`
	FullName  string `json:"fullName"`
	URL       string `json:"url"`
	Private   bool   `json:"private"`
	UpdatedAt string `json:"updatedAt"`
}

// ListRepos lists the authenticated user's repos, most recently updated first.
func (c *GHClient) ListRepos(ctx context.Context) ([]Repo, error) {
	status, b, err := c.do(ctx, http.MethodGet, apiBase+"/user/repos?per_page=100&sort=updated", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error (HTTP %d): %s", status, truncate(strings.TrimSpace(string(b)), 300))
	}
	var raw []struct {
		Name      string `json:"name"`
		FullName  string `json:"full_name"`
		HTMLURL   string `json:"html_url"`
		Private   bool   `json:"private"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(raw))
	for _, r := range raw {
		out = append(out, Repo{
			Name:      r.Name,
			FullName:  r.FullName,
			URL:       r.HTMLURL,
			Private:   r.Private,
			UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// User is the authenticated GitHub user.
type User struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

// GetUser returns the authenticated GitHub user.
func (c *GHClient) GetUser(ctx context.Context) (*User, error) {
	status, b, err := c.do(ctx, http.MethodGet, apiBase+"/user", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error (HTTP %d): %s", status, truncate(strings.TrimSpace(string(b)), 300))
	}
	var raw struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	name := raw.Name
	if name == "" {
		name = raw.Login
	}
	return &User{Login: raw.Login, Name: name, AvatarURL: raw.AvatarURL}, nil
}

// CreateRepo creates a new GitHub repo and returns its clone URL.
func (c *GHClient) CreateRepo(ctx context.Context, name string, private bool) (string, error) {
	status, b, err := c.do(ctx, http.MethodPost, apiBase+"/user/repos",
		map[string]any{"name": name, "private": private})
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("GitHub API error (HTTP %d): %s", status, truncate(strings.TrimSpace(string(b)), 300))
	}
	var resp struct {
		CloneURL string `json:"clone_url"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return "", err
	}
	if resp.CloneURL == "" {
		return "", fmt.Errorf("GitHub did not return a clone URL")
	}
	return resp.CloneURL, nil
}

func basicAuth(token string) *githttp.BasicAuth {
	if token == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: "git", Password: token}
}

// Clone clones repoURL into dest, using token auth for https URLs.
func Clone(ctx context.Context, repoURL, token, dest string) error {
	opts := &git.CloneOptions{URL: repoURL}
	if strings.HasPrefix(repoURL, "http") && token != "" {
		opts.Auth = basicAuth(token)
	}
	_, err := git.PlainCloneContext(ctx, dest, false, opts)
	return err
}

// LinkRepo replaces the contents of dest with a fresh clone of repoURL, so the
// project becomes a working checkout of the linked repository (origin is set,
// so subsequent pushes target it).
func LinkRepo(ctx context.Context, repoURL, token, dest string) error {
	tmp, err := os.MkdirTemp("", "v1-link-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := Clone(ctx, repoURL, token, tmp); err != nil {
		return err
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dest, e.Name())); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest)
}

// Status summarizes the git state of a project directory.
type Status struct {
	IsRepo    bool
	Branch    string
	Modified  int
	Untracked int
	Ahead     int // local commits not yet pushed (outgoing)
	Behind    int // remote commits not yet pulled (incoming)
}

// fetchableSet returns the set of hashes reachable from a commit (deduped).
func reachableSet(repo *git.Repository, start plumbing.Hash) map[plumbing.Hash]bool {
	out := map[plumbing.Hash]bool{}
	it, err := repo.Log(&git.LogOptions{From: start})
	if err != nil {
		return out
	}
	_ = it.ForEach(func(c *object.Commit) error {
		out[c.Hash] = true
		return nil
	})
	return out
}

// statusSync compares HEAD against the remote-tracking branch (as fresh as the
// last fetch/push) and returns the count of outgoing local commits (ahead) and
// incoming remote commits (behind).
func statusSync(repo *git.Repository, branch string) (ahead, behind int) {
	head, err := repo.Head()
	if err != nil {
		return
	}
	refName := plumbing.NewBranchReferenceName(branch)
	if branch == "" {
		refName = head.Name()
	}
	remRefName := plumbing.NewRemoteReferenceName("origin", refName.Short())
	remRef, err := repo.Reference(remRefName, true)
	if err != nil {
		return // no remote-tracking ref (never fetched/pushed) — unknown
	}
	headSet := reachableSet(repo, head.Hash())
	remSet := reachableSet(repo, remRef.Hash())
	for h := range headSet {
		if !remSet[h] {
			ahead++
		}
	}
	for h := range remSet {
		if !headSet[h] {
			behind++
		}
	}
	return
}

// GetStatus inspects the git repository at path.
func GetStatus(path string) Status {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return Status{IsRepo: false}
	}
	st := Status{IsRepo: true}
	if head, err := repo.Head(); err == nil {
		st.Branch = head.Name().Short()
		st.Ahead, st.Behind = statusSync(repo, st.Branch)
	}
	w, err := repo.Worktree()
	if err != nil {
		return st
	}
	ws, err := w.Status()
	if err != nil {
		return st
	}
	for _, fs := range ws {
		switch {
		case fs.Staging == git.Untracked || fs.Worktree == git.Untracked:
			st.Untracked++
		case fs.Staging != git.Unmodified || fs.Worktree != git.Unmodified:
			st.Modified++
		}
	}
	return st
}

func authorSignature(login string) *object.Signature {
	if login == "" {
		return &object.Signature{Name: "v1", Email: "v1@localhost", When: time.Now()}
	}
	return &object.Signature{
		Name:  login,
		Email: login + "@users.noreply.github.com",
		When:  time.Now(),
	}
}

func stageAll(w *git.Worktree) error {
	return w.AddWithOptions(&git.AddOptions{All: true})
}

func hasStagedChanges(w *git.Worktree) (bool, error) {
	ws, err := w.Status()
	if err != nil {
		return false, err
	}
	for _, fs := range ws {
		if fs.Staging != git.Unmodified && fs.Staging != git.Untracked {
			return true, nil
		}
	}
	return false, nil
}

func pushCurrentBranch(ctx context.Context, repo *git.Repository, token string) error {
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("no current branch: %w", err)
	}
	branch := head.Name().Short()
	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	return repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       basicAuth(token),
	})
}

// CommitAndPush stages everything, commits if there are changes, and pushes
// the current branch to origin.
func CommitAndPush(ctx context.Context, path, token, message, authorLogin string) (committed, pushed bool, summary string, err error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return false, false, "", fmt.Errorf("not a git repository")
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, false, "", err
	}
	if err := stageAll(w); err != nil {
		return false, false, "", fmt.Errorf("staging: %w", err)
	}
	dirty, err := hasStagedChanges(w)
	if err != nil {
		return false, false, "", err
	}
	if dirty {
		if strings.TrimSpace(message) == "" {
			message = "Update from v1"
		}
		if _, err := w.Commit(message, &git.CommitOptions{Author: authorSignature(authorLogin)}); err != nil {
			return false, false, "", fmt.Errorf("commit: %w", err)
		}
		committed = true
	}
	err = pushCurrentBranch(ctx, repo, token)
	switch {
	case err == nil:
		pushed = true
		if committed {
			summary = "committed and pushed"
		} else {
			summary = "nothing to commit; pushed existing commits"
		}
	case err == git.NoErrAlreadyUpToDate:
		pushed = true
		if committed {
			summary = "committed; remote already up to date"
		} else {
			summary = "nothing to commit; already up to date"
		}
	default:
		summary = fmt.Sprintf("push failed: %v", err)
	}
	return committed, pushed, summary, nil
}

// Commit stages everything and creates a commit without pushing. Returns the
// new commit hash (empty if there was nothing to commit).
func Commit(ctx context.Context, path, message, authorLogin string) (string, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := stageAll(w); err != nil {
		return "", fmt.Errorf("staging: %w", err)
	}
	dirty, err := hasStagedChanges(w)
	if err != nil {
		return "", err
	}
	if !dirty {
		return "", nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Update from v1"
	}
	h, err := w.Commit(message, &git.CommitOptions{Author: authorSignature(authorLogin)})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return h.String(), nil
}

// Pull fetches and fast-forwards the current branch from origin.
func Pull(ctx context.Context, path, token string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("not a git repository")
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	opts := &git.PullOptions{
		RemoteName: "origin",
		Force:      false,
	}
	if token != "" {
		opts.Auth = basicAuth(token)
	}
	if err := w.PullContext(ctx, opts); err != nil {
		if err == git.NoErrAlreadyUpToDate {
			return nil
		}
		return err
	}
	return nil
}

// InitAndPush initializes a repo if needed, sets the origin remote, commits
// everything and pushes. Used right after creating a GitHub repo.
func InitAndPush(ctx context.Context, path, remoteURL, token, message, authorLogin string) (committed, pushed bool, summary string, err error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		repo, err = git.PlainInit(path, false)
		if err != nil {
			return false, false, "", fmt.Errorf("git init: %w", err)
		}
	}
	if _, err := repo.Remote("origin"); err == nil {
		if err := repo.DeleteRemote("origin"); err != nil {
			return false, false, "", fmt.Errorf("resetting origin: %w", err)
		}
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteURL}}); err != nil {
		return false, false, "", fmt.Errorf("adding origin: %w", err)
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, false, "", err
	}
	if err := stageAll(w); err != nil {
		return false, false, "", fmt.Errorf("staging: %w", err)
	}
	dirty, err := hasStagedChanges(w)
	if err != nil {
		return false, false, "", err
	}
	_, headErr := repo.Head()
	noCommits := headErr != nil
	if dirty || noCommits {
		if strings.TrimSpace(message) == "" {
			message = "Initial commit from v1"
		}
		_, err := w.Commit(message, &git.CommitOptions{
			Author:            authorSignature(authorLogin),
			AllowEmptyCommits: noCommits && !dirty,
		})
		if err != nil {
			return false, false, "", fmt.Errorf("commit: %w", err)
		}
		committed = true
	}
	err = pushCurrentBranch(ctx, repo, token)
	switch {
	case err == nil:
		pushed = true
		summary = "pushed to origin"
	case err == git.NoErrAlreadyUpToDate:
		pushed = true
		summary = "already up to date"
	default:
		summary = fmt.Sprintf("push failed: %v", err)
	}
	return committed, pushed, summary, nil
}

// CommitInfo is a single commit in a project's history.
type CommitInfo struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Time    int64  `json:"time"`
}

// History returns the commit log of the repo at path, newest first.
func History(path string) ([]CommitInfo, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := []CommitInfo{}
	err = iter.ForEach(func(c *object.Commit) error {
		author := c.Author.Name
		if author == "" {
			author = "unknown"
		}
		out = append(out, CommitInfo{
			Hash:    c.Hash.String(),
			Short:   c.Hash.String()[:8],
			Message: c.Message,
			Author:  author,
			Time:    c.Author.When.Unix(),
		})
		return nil
	})
	return out, err
}

// Branches returns the current branch name and the sorted list of all local
// branches in the repo at path.
func Branches(path string) (current string, branches []string, err error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return "", nil, err
	}
	head, err := repo.Head()
	if err == nil {
		current = head.Name().Short()
	}
	iter, err := repo.Branches()
	if err != nil {
		return current, nil, err
	}
	defer iter.Close()
	branches = []string{}
	err = iter.ForEach(func(b *plumbing.Reference) error {
		branches = append(branches, b.Name().Short())
		return nil
	})
	sort.Strings(branches)
	return current, branches, err
}

// projectIgnore is written on git init so node_modules and other generated
// output never get committed.
const projectIgnore = "node_modules/\ndist/\nbuild/\n*.log\n.env\n.v1/\n"

// InitRepo turns path into a git repository if it is not already one. An
// existing repository is left untouched. A brand-new repository gets an
// initial commit so time-travel has a starting point.
func InitRepo(path, authorLogin string) error {
	if _, err := git.PlainOpen(path); err == nil {
		return nil
	}
	ignorePath := filepath.Join(path, ".gitignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		if err := os.WriteFile(ignorePath, []byte(projectIgnore), 0o644); err != nil {
			return err
		}
	}
	repo, err := git.PlainInit(path, false)
	if err != nil {
		return err
	}
	if _, err := repo.Head(); err == nil {
		return nil // already has commits
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := stageAll(w); err != nil {
		return err
	}
	_, err = w.Commit("Initial commit from v1", &git.CommitOptions{
		Author:            authorSignature(authorLogin),
		AllowEmptyCommits: true,
	})
	return err
}

// CreateBranch creates and checks out a new branch named name.
func CreateBranch(path, name string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	return w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	})
}

// CheckoutBranch switches the working tree to an existing branch, keeping any
// local modifications.
func CheckoutBranch(path, name string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	return w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Keep:   true,
	})
}

// RevertTo hard-resets the working tree at path to the given commit, dropping
// every change made after it. This is the "rewind that actually changes the
// repo" operation.
func RevertTo(path, commit string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}
	h, err := repo.ResolveRevision(plumbing.Revision(commit))
	if err != nil {
		return err
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	return w.Reset(&git.ResetOptions{Commit: *h, Mode: git.HardReset})
}

// CommitIfRepo stages and commits any changes at path when it is a git
// repository. It returns whether a commit was made, and never fails when path
// is not a repo (no error, no commit).
func CommitIfRepo(path, message, authorLogin string) (bool, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return false, nil
	}
	w, err := repo.Worktree()
	if err != nil {
		return false, err
	}
	if err := stageAll(w); err != nil {
		return false, err
	}
	dirty, err := hasStagedChanges(w)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Update from v1"
	}
	_, err = w.Commit(message, &git.CommitOptions{Author: authorSignature(authorLogin)})
	return err == nil, err
}
