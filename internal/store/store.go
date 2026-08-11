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
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint rejects a row (duplicate
// username, etc.).
var ErrConflict = errors.New("conflict")

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
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_settings (
  user_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (user_id, key)
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
  instructions TEXT,
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
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id, id);
`)
	if err != nil {
		return err
	}
	// Columns added after their tables first shipped, for existing databases.
	if err := migrateAddColumns(db, "messages", map[string]string{
		"model":       "ALTER TABLE messages ADD COLUMN model TEXT",
		"reasoning":   "ALTER TABLE messages ADD COLUMN reasoning TEXT",
		"usage":       "ALTER TABLE messages ADD COLUMN usage TEXT",
		"attachments": "ALTER TABLE messages ADD COLUMN attachments TEXT",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "projects", map[string]string{
		"instructions": "ALTER TABLE projects ADD COLUMN instructions TEXT",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "sessions", map[string]string{
		"user_id": "ALTER TABLE sessions ADD COLUMN user_id TEXT",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "projects", map[string]string{
		"owner_id": "ALTER TABLE projects ADD COLUMN owner_id TEXT",
	}); err != nil {
		return err
	}
	if err := migrateLegacyUsers(db); err != nil {
		return err
	}
	return migrateAddColumns(db, "memories", map[string]string{
		"enabled": "ALTER TABLE memories ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1",
	})
}

// migrateLegacyUsers converts a pre-multi-user database: the single stored
// password becomes the admin user, anonymous sessions are dropped, and
// ownerless projects are claimed by an admin (or the oldest user). Idempotent.
func migrateLegacyUsers(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id IS NULL`); err != nil {
		return err
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	ownerID := ""
	if count == 0 {
		var hash string
		err := db.QueryRow(`SELECT value FROM settings WHERE key = 'password_hash'`).Scan(&hash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // setup screen still required
		}
		if err != nil {
			return err
		}
		ownerID = NewID()
		if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, is_admin, created_at) VALUES (?, 'admin', ?, 1, ?)`,
			ownerID, hash, now()); err != nil {
			return err
		}
		if _, err := db.Exec(`DELETE FROM settings WHERE key = 'password_hash'`); err != nil {
			return err
		}
	} else {
		// Ownerless projects (e.g. created while auth was disabled) go to the
		// first admin, or the oldest user when no admin exists.
		err := db.QueryRow(`SELECT id FROM users WHERE is_admin = 1 ORDER BY created_at LIMIT 1`).Scan(&ownerID)
		if errors.Is(err, sql.ErrNoRows) {
			ownerID = ""
		} else if err != nil {
			return err
		}
		if ownerID == "" {
			err = db.QueryRow(`SELECT id FROM users ORDER BY created_at LIMIT 1`).Scan(&ownerID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`UPDATE projects SET owner_id = ? WHERE owner_id IS NULL`, ownerID)
	return err
}

// migrateAddColumns ALTERs the given columns onto table when missing.
func migrateAddColumns(db *sql.DB, table string, want map[string]string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
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
	for name, ddl := range want {
		if have[name] {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
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

// ---- users ----

// User is a row of the users table.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    int64
}

// CreateUser inserts a user; ErrConflict when the username is taken.
func (s *Store) CreateUser(u *User) error {
	if _, err := s.db.Exec(`INSERT INTO users (id, username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, boolInt(u.IsAdmin), now()); err != nil {
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// GetUserByUsername returns a user by exact username, or ErrNotFound.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = ?`, username))
}

// GetUserByID returns a user by id, or ErrNotFound.
func (s *Store) GetUserByID(id string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE id = ?`, id))
}

// ListUsers returns all users, oldest first.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, is_admin, created_at FROM users ORDER BY created_at, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserPassword replaces a user's password hash, or returns ErrNotFound.
func (s *Store) SetUserPassword(id, hash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserAdmin flips a user's admin flag, or returns ErrNotFound.
func (s *Store) SetUserAdmin(id string, isAdmin bool) error {
	res, err := s.db.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, boolInt(isAdmin), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminCount returns the number of admin users.
func (s *Store) AdminCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return n, err
}

// DeleteUser removes a user, their user settings and their projects (rows
// only — the caller removes the project directories).
func (s *Store) DeleteUser(id string) error {
	projects, err := s.ListProjectsByOwner(id)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := s.DeleteProject(p.ID); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ?`, id); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// UserCount returns the number of users.
func (s *Store) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ClaimOwnerlessProjects assigns every project without an owner (legacy
// rows, or projects created while auth was disabled) to ownerID.
func (s *Store) ClaimOwnerlessProjects(ownerID string) error {
	_, err := s.db.Exec(`UPDATE projects SET owner_id = ? WHERE owner_id IS NULL OR owner_id = ''`, ownerID)
	return err
}

// ---- user settings ----

// GetUserSetting returns a user's value for key and whether it exists.
// Global settings (shared) act as the fallback layer below these.
func (s *Store) GetUserSetting(userID, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM user_settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetUserSetting upserts a user's settings value.
func (s *Store) SetUserSetting(userID, key, value string) error {
	_, err := s.db.Exec(`INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, key, value)
	return err
}

// DeleteUserSetting removes a user's settings key.
func (s *Store) DeleteUserSetting(userID, key string) error {
	_, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ? AND key = ?`, userID, key)
	return err
}

func scanUser(row scanner) (*User, error) {
	var u User
	var isAdmin int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.IsAdmin = isAdmin != 0
	return &u, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
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

// CreateSession stores a session token for a user with the given TTL.
func (s *Store) CreateSession(token, userID string, ttl time.Duration) error {
	t := time.Now()
	_, err := s.db.Exec(`INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, t.Unix(), t.Add(ttl).Unix())
	return err
}

// SessionUser returns the user id of a valid, unexpired session token, or
// ("", false) when the token is missing, expired or orphaned. Expired and
// orphaned rows are cleaned up on the way.
func (s *Store) SessionUser(token string) (string, bool, error) {
	var userID sql.NullString
	var expires int64
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !userID.Valid || time.Now().Unix() > expires {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return "", false, nil
	}
	return userID.String, true, nil
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
	Instructions   string
	OwnerID        string
	CreatedAt      int64
	UpdatedAt      int64
}

// CreateProject inserts a project, stamping created_at/updated_at.
func (s *Store) CreateProject(p *Project) error {
	t := now()
	p.CreatedAt = t
	p.UpdatedAt = t
	_, err := s.db.Exec(`INSERT INTO projects (id, name, path, repo_url, preview_command, instructions, owner_id, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		p.ID, p.Name, p.Path, p.RepoURL, p.PreviewCommand, p.Instructions, p.OwnerID, p.CreatedAt, p.UpdatedAt)
	return err
}

type scanner interface {
	Scan(...any) error
}

func scanProject(row scanner) (*Project, error) {
	var p Project
	var repoURL, previewCmd, instructions, ownerID sql.NullString
	if err := row.Scan(&p.ID, &p.Name, &p.Path, &repoURL, &previewCmd, &instructions, &ownerID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.RepoURL = repoURL.String
	p.PreviewCommand = previewCmd.String
	p.Instructions = instructions.String
	p.OwnerID = ownerID.String
	return &p, nil
}

const projectCols = `id, name, path, repo_url, preview_command, instructions, owner_id, created_at, updated_at`

// UpdateProjectSettings rewrites the user-editable project fields.
func (s *Store) UpdateProjectSettings(id, name, previewCommand, instructions string) error {
	_, err := s.db.Exec(`UPDATE projects SET name = ?, preview_command = NULLIF(?, ''), instructions = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		name, previewCommand, instructions, now(), id)
	return err
}

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
	return s.listProjects(`SELECT ` + projectCols + ` FROM projects ORDER BY updated_at DESC`, nil)
}

// ListProjectsByOwner returns a user's projects sorted by updated_at
// descending.
func (s *Store) ListProjectsByOwner(ownerID string) ([]*Project, error) {
	return s.listProjects(`SELECT `+projectCols+` FROM projects WHERE owner_id = ? ORDER BY updated_at DESC`, ownerID)
}

func (s *Store) listProjects(query string, arg any) ([]*Project, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if arg == nil {
		rows, err = s.db.Query(query)
	} else {
		rows, err = s.db.Query(query, arg)
	}
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

// DeleteProject removes a project and its messages, memories and snapshots.
func (s *Store) DeleteProject(id string) error {
	if _, err := s.db.Exec(`DELETE FROM messages WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM memories WHERE project_id = ?`, id); err != nil {
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
	Enabled   bool   `json:"enabled"`
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
	rows, err := s.db.Query(`SELECT id, content, enabled, created_at FROM memories WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Enabled, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMemoryEnabled toggles a memory without deleting it, or ErrNotFound.
func (s *Store) SetMemoryEnabled(projectID string, id int64, enabled bool) error {
	res, err := s.db.Exec(`UPDATE memories SET enabled = ? WHERE project_id = ? AND id = ?`, enabled, projectID, id)
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

// UpdateMemory rewrites a memory's content, or returns ErrNotFound.
func (s *Store) UpdateMemory(projectID string, id int64, content string) error {
	res, err := s.db.Exec(`UPDATE memories SET content = ? WHERE project_id = ? AND id = ?`, content, projectID, id)
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
