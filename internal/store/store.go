// Package store wraps the SQLite database holding all v1 state.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store provides typed access to the v1 database.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the database at $DATA_DIR/v1.db.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "projects"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "v1.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  created_at INTEGER,
  expires_at INTEGER
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  path TEXT NOT NULL,
  repo_url TEXT,
  preview_command TEXT,
  created_at INTEGER,
  updated_at INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_json TEXT,
  created_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_project_id ON messages(project_id, id);
`)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NewID returns a random 12-character hex ID.
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func now() int64 { return time.Now().Unix() }

// ---- settings ----

// GetSetting returns the value for key and whether it exists.
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting upserts a settings value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes a settings key.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

// ---- sessions ----

// CreateSession stores a session token with the given TTL.
func (s *Store) CreateSession(token string, ttl time.Duration) error {
	t := time.Now()
	_, err := s.db.Exec(`INSERT INTO sessions (token, created_at, expires_at) VALUES (?, ?, ?)`,
		token, t.Unix(), t.Add(ttl).Unix())
	return err
}

// SessionValid reports whether the token exists and has not expired.
func (s *Store) SessionValid(token string) (bool, error) {
	var expires int64
	err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().Unix() > expires {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return false, nil
	}
	return true, nil
}

// DeleteSession removes a session token.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// ---- projects ----

// Project is a row of the projects table.
type Project struct {
	ID             string
	Name           string
	Path           string
	RepoURL        string
	PreviewCommand string
	CreatedAt      int64
	UpdatedAt      int64
}

// CreateProject inserts a project, stamping created_at/updated_at.
func (s *Store) CreateProject(p *Project) error {
	t := now()
	p.CreatedAt = t
	p.UpdatedAt = t
	_, err := s.db.Exec(`INSERT INTO projects (id, name, path, repo_url, preview_command, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		p.ID, p.Name, p.Path, p.RepoURL, p.PreviewCommand, p.CreatedAt, p.UpdatedAt)
	return err
}

type scanner interface {
	Scan(...any) error
}

func scanProject(row scanner) (*Project, error) {
	var p Project
	var repoURL, previewCmd sql.NullString
	if err := row.Scan(&p.ID, &p.Name, &p.Path, &repoURL, &previewCmd, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.RepoURL = repoURL.String
	p.PreviewCommand = previewCmd.String
	return &p, nil
}

const projectCols = `id, name, path, repo_url, preview_command, created_at, updated_at`

// GetProject returns the project with the given ID or ErrNotFound.
func (s *Store) GetProject(id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListProjects returns all projects sorted by updated_at descending.
func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProject removes a project and its messages.
func (s *Store) DeleteProject(id string) error {
	if _, err := s.db.Exec(`DELETE FROM messages WHERE project_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// TouchProject bumps updated_at to now.
func (s *Store) TouchProject(id string) error {
	_, err := s.db.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now(), id)
	return err
}

// SetProjectRepoURL updates the repo_url column.
func (s *Store) SetProjectRepoURL(id, repoURL string) error {
	_, err := s.db.Exec(`UPDATE projects SET repo_url = NULLIF(?, ''), updated_at = ? WHERE id = ?`, repoURL, now(), id)
	return err
}

// ---- messages ----

// Message is a row of the messages table.
type Message struct {
	ID        int64
	ProjectID string
	Role      string
	Content   string
	ToolJSON  string
	CreatedAt int64
}

// AddMessage appends a message to a project's history.
func (s *Store) AddMessage(projectID, role, content, toolJSON string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO messages (project_id, role, content, tool_json, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?)`, projectID, role, content, toolJSON, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListMessages returns a project's messages in insertion order.
func (s *Store) ListMessages(projectID string) ([]*Message, error) {
	rows, err := s.db.Query(`SELECT id, project_id, role, content, tool_json, created_at
		FROM messages WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		var m Message
		var toolJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Role, &m.Content, &toolJSON, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ToolJSON = toolJSON.String
		out = append(out, &m)
	}
	return out, rows.Err()
}
