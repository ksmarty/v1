// Package store wraps the SQLite database holding all v1 state.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
CREATE TABLE IF NOT EXISTS compaction_snapshots (
  project_id TEXT PRIMARY KEY,
  summary TEXT NOT NULL,
  covered_message_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id, id);
`)
	if err != nil {
		return err
	}
	return migrateMessages(db)
}

// migrateMessages adds columns added after the initial schema so existing
// databases survive upgrades. Each missing column is ALTERed onto messages.
func migrateMessages(db *sql.DB) error {
	want := []struct {
		name string
		ddl  string
	}{
		{"model", "ALTER TABLE messages ADD COLUMN model TEXT"},
		{"reasoning", "ALTER TABLE messages ADD COLUMN reasoning TEXT"},
		{"usage", "ALTER TABLE messages ADD COLUMN usage TEXT"},
		{"attachments", "ALTER TABLE messages ADD COLUMN attachments TEXT"},
	}
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, c := range want {
		if have[c.name] {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return err
		}
	}
	return nil
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

// ---- todos ----

// Todo is one item in a project's task list maintained by the agent.
type Todo struct {
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// todoSettingKey returns the settings key backing a project's todo list.
func todoSettingKey(projectID string) string { return "project_todos:" + projectID }

// GetTodos returns a project's todo list (empty when none was ever set).
func (s *Store) GetTodos(projectID string) ([]Todo, error) {
	v, ok, err := s.GetSetting(todoSettingKey(projectID))
	if err != nil || !ok || v == "" {
		return []Todo{}, err
	}
	var out []Todo
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return []Todo{}, nil
	}
	return out, nil
}

// SetTodos stores a project's todo list.
func (s *Store) SetTodos(projectID string, todos []Todo) error {
	if todos == nil {
		todos = []Todo{}
	}
	data, err := json.Marshal(todos)
	if err != nil {
		return err
	}
	return s.SetSetting(todoSettingKey(projectID), string(data))
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
	if _, err := s.db.Exec(`DELETE FROM compaction_snapshots WHERE project_id = ?`, id); err != nil {
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

// Message is a row of the messages table. Attachments holds the JSON-encoded
// attachment list of a user message (see agent.Attachment) — always empty for
// assistant and tool rows.
type Message struct {
	ID          int64
	ProjectID   string
	Role        string
	Content     string
	ToolJSON    string
	Model       string
	Reasoning   string
	Usage       string
	Attachments string
	CreatedAt   int64
}

// AddMessage appends a message to a project's history.
func (s *Store) AddMessage(projectID, role, content, toolJSON, model, reasoning, usage, attachments string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO messages (project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
		projectID, role, content, toolJSON, model, reasoning, usage, attachments, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CompactionSnapshot is a non-visible summary covering messages through an ID.
type CompactionSnapshot struct {
	ProjectID        string
	Summary          string
	CoveredMessageID int64
	CreatedAt        int64
}

// GetCompactionSnapshot returns the latest snapshot for a project.
func (s *Store) GetCompactionSnapshot(projectID string) (*CompactionSnapshot, error) {
	var c CompactionSnapshot
	err := s.db.QueryRow(`SELECT project_id, summary, covered_message_id, created_at FROM compaction_snapshots WHERE project_id = ?`, projectID).
		Scan(&c.ProjectID, &c.Summary, &c.CoveredMessageID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// SaveCompactionSnapshot replaces the project's non-visible transcript summary.
func (s *Store) SaveCompactionSnapshot(projectID, summary string, coveredMessageID int64) error {
	_, err := s.db.Exec(`INSERT INTO compaction_snapshots (project_id, summary, covered_message_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET summary = excluded.summary, covered_message_id = excluded.covered_message_id, created_at = excluded.created_at`,
		projectID, summary, coveredMessageID, now())
	return err
}

// DeleteMessagesAfter removes every message with id greater than id. Chat
// retry uses it to drop the aborted turn following the last user message.
func (s *Store) DeleteMessagesAfter(projectID string, id int64) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE project_id = ? AND id > ?`, projectID, id)
	return err
}

// Memory is one fact the agent saved for a project.
type Memory struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"createdAt"`
}

// AddMemory stores a memory for a project and returns its id.
func (s *Store) AddMemory(projectID, content string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO memories (project_id, content, created_at) VALUES (?, ?, ?)`,
		projectID, content, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListMemories returns a project's memories oldest first.
func (s *Store) ListMemories(projectID string) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT id, content, created_at FROM memories WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMemory removes one memory, or returns ErrNotFound.
func (s *Store) DeleteMemory(projectID string, id int64) error {
	res, err := s.db.Exec(`DELETE FROM memories WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMessage returns the message with the given id for a project, or
// ErrNotFound when it does not exist.
func (s *Store) GetMessage(projectID string, id int64) (*Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT id, project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at
		FROM messages WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// UpdateMessageContent rewrites a message's content, or returns ErrNotFound
// when no such message exists for the project.
func (s *Store) UpdateMessageContent(projectID string, id int64, content string) error {
	res, err := s.db.Exec(`UPDATE messages SET content = ? WHERE project_id = ? AND id = ?`, content, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LastUserMessage returns the most recent user message of a project, or
// ErrNotFound when the project has none.
func (s *Store) LastUserMessage(projectID string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT id, project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at
		FROM messages WHERE project_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func scanMessage(row scanner) (*Message, error) {
	var m Message
	var toolJSON, model, reasoning, usage, attachments sql.NullString
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Role, &m.Content, &toolJSON, &model, &reasoning, &usage, &attachments, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.ToolJSON = toolJSON.String
	m.Model = model.String
	m.Reasoning = reasoning.String
	m.Usage = usage.String
	m.Attachments = attachments.String
	return &m, nil
}

// ListMessages returns a project's messages in insertion order.
func (s *Store) ListMessages(projectID string) ([]*Message, error) {
	rows, err := s.db.Query(`SELECT id, project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at
		FROM messages WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
