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
  session_id TEXT,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_json TEXT,
  created_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_project_id ON messages(project_id, id);
CREATE TABLE IF NOT EXISTS chat_sessions (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chat_sessions_project ON chat_sessions(project_id);
CREATE TABLE IF NOT EXISTS compaction_snapshots (
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL,
  covered_message_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, session_id)
);
CREATE TABLE IF NOT EXISTS memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  content TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  category TEXT NOT NULL DEFAULT 'fact',
  importance REAL NOT NULL DEFAULT 1,
  last_accessed INTEGER NOT NULL DEFAULT 0,
  access_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id, id);
CREATE TABLE IF NOT EXISTS plans (
  project_id TEXT PRIMARY KEY,
  plan_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pending_asks (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  question TEXT NOT NULL,
  options TEXT NOT NULL DEFAULT '[]',
  questions TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, session_id)
);
CREATE TABLE IF NOT EXISTS vercel_deploys (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  state TEXT NOT NULL,
  error TEXT,
  deployment_id TEXT,
  url TEXT,
  target TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
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
		"session_id":  "ALTER TABLE messages ADD COLUMN session_id TEXT",
		"checkpoint":  "ALTER TABLE messages ADD COLUMN checkpoint TEXT",
	}); err != nil {
		return err
	}
	// Chat sessions split each project's history into independent threads.
	// Pre-session rows belong to the project's default session, created on
	// first access; compaction snapshots and pending asks become per-session.
	if err := rebuildTableForSessions(db, "compaction_snapshots", `
CREATE TABLE compaction_snapshots_v2 (
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL,
  covered_message_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, session_id)
)`,
		`SELECT project_id, '', summary, covered_message_id, created_at FROM compaction_snapshots`); err != nil {
		return err
	}
	if err := rebuildTableForSessions(db, "pending_asks", `
CREATE TABLE pending_asks_v2 (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  question TEXT NOT NULL,
  options TEXT NOT NULL DEFAULT '[]',
  questions TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, session_id)
)`,
		`SELECT id, project_id, '', question, options, NULL, created_at FROM pending_asks`); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "projects", map[string]string{
		"instructions": "ALTER TABLE projects ADD COLUMN instructions TEXT",
		"auto_push":    "ALTER TABLE projects ADD COLUMN auto_push INTEGER DEFAULT 0",
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
	if err := migrateAddColumns(db, "pending_asks", map[string]string{
		"questions": "ALTER TABLE pending_asks ADD COLUMN questions TEXT",
	}); err != nil {
		return err
	}
	if err := migrateLegacyUsers(db); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "memories", map[string]string{
		"enabled": "ALTER TABLE memories ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "memories", map[string]string{
		"category":      "ALTER TABLE memories ADD COLUMN category TEXT NOT NULL DEFAULT 'fact'",
		"importance":    "ALTER TABLE memories ADD COLUMN importance REAL NOT NULL DEFAULT 1",
		"last_accessed": "ALTER TABLE memories ADD COLUMN last_accessed INTEGER NOT NULL DEFAULT 0",
		"access_count":  "ALTER TABLE memories ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "users", map[string]string{
		"oidc": "ALTER TABLE users ADD COLUMN oidc INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	if err := migrateAddColumns(db, "chat_sessions", map[string]string{
		"archived": "ALTER TABLE chat_sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	return migrateAddColumns(db, "projects", map[string]string{
		"preview_disabled": "ALTER TABLE projects ADD COLUMN preview_disabled INTEGER NOT NULL DEFAULT 0",
	})
}

// rebuildTableForSessions moves a pre-session table to the per-session shape
// when it still lacks a session_id column: create the new table, copy the old
// rows with an empty session, drop the old table, rename. Idempotent.
func rebuildTableForSessions(db *sql.DB, table, createSQL, copySQL string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('` + table + `') WHERE name = 'session_id'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(createSQL + `;
INSERT INTO ` + table + `_v2 ` + copySQL + `;
DROP TABLE ` + table + `;
ALTER TABLE ` + table + `_v2 RENAME TO ` + table + `;`); err != nil {
		return err
	}
	return nil
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
	OIDC         bool
	CreatedAt    int64
}

// CreateUser inserts a user; ErrConflict when the username is taken.
func (s *Store) CreateUser(u *User) error {
	if _, err := s.db.Exec(`INSERT INTO users (id, username, password_hash, is_admin, oidc, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, boolInt(u.IsAdmin), boolInt(u.OIDC), now()); err != nil {
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// GetUserByUsername returns a user by exact username, or ErrNotFound.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT id, username, password_hash, is_admin, oidc, created_at FROM users WHERE username = ?`, username))
}

// GetUserByID returns a user by id, or ErrNotFound.
func (s *Store) GetUserByID(id string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT id, username, password_hash, is_admin, oidc, created_at FROM users WHERE id = ?`, id))
}

// ListUsers returns all users, oldest first.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, is_admin, oidc, created_at FROM users ORDER BY created_at, username`)
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

// SetUserOIDC marks whether a user authenticates via OIDC, or ErrNotFound.
func (s *Store) SetUserOIDC(id string, oidc bool) error {
	res, err := s.db.Exec(`UPDATE users SET oidc = ? WHERE id = ?`, boolInt(oidc), id)
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

// SeedUserSetting sets value for key on every user that has no value for it
// yet. Used to roll a global default out to existing accounts without
// clobbering their own choices.
func (s *Store) SeedUserSetting(key, value string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO user_settings (user_id, key, value)
		SELECT id, ?, ? FROM users
		WHERE NOT EXISTS (SELECT 1 FROM user_settings WHERE user_id = users.id AND key = ?)`,
		key, value, key)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteUserSetting removes a user's settings key.
func (s *Store) DeleteUserSetting(userID, key string) error {
	_, err := s.db.Exec(`DELETE FROM user_settings WHERE user_id = ? AND key = ?`, userID, key)
	return err
}

func scanUser(row scanner) (*User, error) {
	var u User
	var isAdmin, oidc int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &oidc, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.IsAdmin = isAdmin != 0
	u.OIDC = oidc != 0
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

// ---- pending asks ----

// PendingAsk is a question from the agent's ask_user tool awaiting an answer.
// Persisted so the question survives app reloads and server restarts — the
// live channel (askRegistry) is in-memory, this is the durable record.
type PendingAsk struct {
	ID       string
	Question string
	Options  []string
	// QuestionsJSON holds the full questions array for multi-question asks
	// (empty for a single question, which lives in Question/Options).
	QuestionsJSON string
}

// SetPendingAsk records the project's pending ask_user question, replacing
// any previous one (only one can be pending per project).
func (s *Store) SetPendingAsk(projectID, sessionID, id, question string, options []string, questionsJSON string) error {
	if options == nil {
		options = []string{}
	}
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO pending_asks (id, project_id, session_id, question, options, questions, created_at) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?)
		ON CONFLICT(project_id, session_id) DO UPDATE SET id = excluded.id, question = excluded.question, options = excluded.options, questions = excluded.questions, created_at = excluded.created_at`,
		id, projectID, sessionID, question, string(data), questionsJSON, time.Now().Unix())
	return err
}

// GetPendingAsk returns the project's pending ask, or ErrNotFound when none.
func (s *Store) GetPendingAsk(projectID, sessionID string) (*PendingAsk, error) {
	var a PendingAsk
	var opts string
	err := s.db.QueryRow(`SELECT id, question, options, COALESCE(questions, '') FROM pending_asks WHERE project_id = ? AND session_id = ?`, projectID, sessionID).
		Scan(&a.ID, &a.Question, &opts, &a.QuestionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(opts), &a.Options); err != nil {
		a.Options = nil
	}
	return &a, nil
}

// ClearPendingAsk removes the project's pending ask (answered, timed out, or
// superseded by a new turn).
func (s *Store) ClearPendingAsk(projectID, sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM pending_asks WHERE project_id = ? AND session_id = ?`, projectID, sessionID)
	return err
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
	ID              string
	Name            string
	Path            string
	RepoURL         string
	PreviewCommand  string
	Instructions    string
	OwnerID         string
	AutoPush        bool
	PreviewDisabled bool
	CreatedAt       int64
	UpdatedAt       int64
}

// CreateProject inserts a project, stamping created_at/updated_at.
func (s *Store) CreateProject(p *Project) error {
	t := now()
	p.CreatedAt = t
	p.UpdatedAt = t
	_, err := s.db.Exec(`INSERT INTO projects (id, name, path, repo_url, preview_command, instructions, owner_id, auto_push, preview_disabled, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.RepoURL, p.PreviewCommand, p.Instructions, p.OwnerID, boolInt(p.AutoPush), boolInt(p.PreviewDisabled), p.CreatedAt, p.UpdatedAt)
	return err
}

type scanner interface {
	Scan(...any) error
}

func scanProject(row scanner) (*Project, error) {
	var p Project
	var repoURL, previewCmd, instructions, ownerID sql.NullString
	var autoPush, previewDisabled int
	if err := row.Scan(&p.ID, &p.Name, &p.Path, &repoURL, &previewCmd, &instructions, &ownerID, &autoPush, &previewDisabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.RepoURL = repoURL.String
	p.PreviewCommand = previewCmd.String
	p.Instructions = instructions.String
	p.OwnerID = ownerID.String
	p.AutoPush = autoPush != 0
	p.PreviewDisabled = previewDisabled != 0
	return &p, nil
}

const projectCols = `id, name, path, repo_url, preview_command, instructions, owner_id, auto_push, preview_disabled, created_at, updated_at`

// UpdateProjectAutoPush toggles the per-project auto-push flag.
func (s *Store) UpdateProjectAutoPush(id string, autoPush bool) error {
	_, err := s.db.Exec(`UPDATE projects SET auto_push = ?, updated_at = ? WHERE id = ?`, boolInt(autoPush), now(), id)
	return err
}

// UpdateProjectPreviewDisabled toggles the per-project preview-disabled flag.
func (s *Store) UpdateProjectPreviewDisabled(id string, disabled bool) error {
	_, err := s.db.Exec(`UPDATE projects SET preview_disabled = ?, updated_at = ? WHERE id = ?`, boolInt(disabled), now(), id)
	return err
}

// UpdateProjectSettings rewrites the user-editable project fields.
func (s *Store) UpdateProjectSettings(id, name, previewCommand, instructions string) error {
	_, err := s.db.Exec(`UPDATE projects SET name = ?, preview_command = NULLIF(?, ''), instructions = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		name, previewCommand, instructions, now(), id)
	return err
}

// RenameProject updates just the project's display name (the agent's
// set_project_name tool).
func (s *Store) RenameProject(id, name string) error {
	_, err := s.db.Exec(`UPDATE projects SET name = ?, updated_at = ? WHERE id = ?`, name, now(), id)
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
	return s.listProjects(`SELECT `+projectCols+` FROM projects ORDER BY updated_at DESC`, nil)
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
	if _, err := s.db.Exec(`DELETE FROM chat_sessions WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM memories WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM compaction_snapshots WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM pending_asks WHERE project_id = ?`, id); err != nil {
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

// AddMessage appends a message to a chat session's history.
func (s *Store) AddMessage(projectID, sessionID, role, content, toolJSON, model, reasoning, usage, attachments string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO messages (project_id, session_id, role, content, tool_json, model, reasoning, usage, attachments, created_at)
		VALUES (?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
		projectID, sessionID, role, content, toolJSON, model, reasoning, usage, attachments, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetMessageCheckpoint stamps a turn's user message with the git checkpoint
// hash (the post-turn snapshot commit), linking chat history to a specific
// revision the user can revert to.
func (s *Store) SetMessageCheckpoint(id int64, hash string) error {
	if hash == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE messages SET checkpoint = ? WHERE id = ?`, hash, id)
	return err
}

// LatestUserMessageID returns the newest user message in a session, used to
// attach the checkpoint hash after a turn completes.
func (s *Store) LatestUserMessageID(projectID, sessionID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM messages WHERE project_id = ? AND session_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`, projectID, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

const (
	VercelDeployBuilding = "BUILDING"
	VercelDeployReady    = "READY"
	VercelDeployError    = "ERROR"
	VercelDeployCanceled = "CANCELED"
)

// VercelDeploy is one persisted deployment attempt. Deployments start as
// BUILDING, then settle into a terminal state; rows survive server restarts
// so the UI never loses an in-flight (or interrupted) deploy.
type VercelDeploy struct {
	ID           string
	ProjectID    string
	UserID       string
	State        string
	Error        string
	DeploymentID string
	URL          string
	Target       string
	CreatedAt    int64
	UpdatedAt    int64
}

// CreateVercelDeploy records a deploy attempt as BUILDING.
func (s *Store) CreateVercelDeploy(id, projectID, userID, target string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO vercel_deploys (id, project_id, user_id, state, target, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, projectID, userID, VercelDeployBuilding, target, now, now)
	return err
}

// UpdateVercelDeploy settles a deploy into its terminal state with whatever
// the Vercel API reported (deployment id/url or the error text).
func (s *Store) UpdateVercelDeploy(id, state, errText, deploymentID, url string) error {
	_, err := s.db.Exec(`UPDATE vercel_deploys SET state = ?, error = ?, deployment_id = ?, url = ?, updated_at = ? WHERE id = ?`,
		state, errText, deploymentID, url, time.Now().Unix(), id)
	return err
}

// LatestVercelDeploy returns the most recent deploy row for a project+user,
// or nil when there is none. Used to resurrect deploy status after a restart.
func (s *Store) LatestVercelDeploy(projectID, userID string) (*VercelDeploy, error) {
	row := s.db.QueryRow(`SELECT id, project_id, user_id, state, COALESCE(error,''), COALESCE(deployment_id,''), COALESCE(url,''), COALESCE(target,''), created_at, updated_at
		FROM vercel_deploys WHERE project_id = ? AND user_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, projectID, userID)
	var d VercelDeploy
	err := row.Scan(&d.ID, &d.ProjectID, &d.UserID, &d.State, &d.Error, &d.DeploymentID, &d.URL, &d.Target, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// FailInterruptedVercelDeploys marks every still-building deploy as ERROR
// with the given reason — called once at startup, since a process that died
// mid-deploy can never finish its in-flight uploads.
func (s *Store) FailInterruptedVercelDeploys(reason string) error {
	_, err := s.db.Exec(`UPDATE vercel_deploys SET state = ?, error = ?, updated_at = ? WHERE state = ?`,
		VercelDeployError, reason, time.Now().Unix(), VercelDeployBuilding)
	return err
}

// CompactionSnapshot is a non-visible summary covering messages through an ID.
type CompactionSnapshot struct {
	ProjectID        string
	SessionID        string
	Summary          string
	CoveredMessageID int64
	CreatedAt        int64
}

// GetCompactionSnapshot returns the latest snapshot for a chat session.
func (s *Store) GetCompactionSnapshot(projectID, sessionID string) (*CompactionSnapshot, error) {
	var c CompactionSnapshot
	err := s.db.QueryRow(`SELECT project_id, session_id, summary, covered_message_id, created_at FROM compaction_snapshots WHERE project_id = ? AND session_id = ?`, projectID, sessionID).
		Scan(&c.ProjectID, &c.SessionID, &c.Summary, &c.CoveredMessageID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// SaveCompactionSnapshot replaces the chat session's non-visible transcript
// summary.
func (s *Store) SaveCompactionSnapshot(projectID, sessionID, summary string, coveredMessageID int64) error {
	_, err := s.db.Exec(`INSERT INTO compaction_snapshots (project_id, session_id, summary, covered_message_id, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, session_id) DO UPDATE SET summary = excluded.summary, covered_message_id = excluded.covered_message_id, created_at = excluded.created_at`,
		projectID, sessionID, summary, coveredMessageID, now())
	return err
}

// MergeContinuedTurn folds a continued assistant reply back into the partial
// it continued from: content and reasoning are concatenated, the continuation
// message's usage moves to the partial, and both the continuation and the
// error row that followed the partial are deleted. Returns the partial's id.
func (s *Store) MergeContinuedTurn(projectID, sessionID string, partialID int64) (int64, error) {
	partial, err := s.GetMessage(projectID, partialID)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.Query(`SELECT id, role, content, reasoning, usage FROM messages
		WHERE project_id = ? AND session_id = ? AND id > ? ORDER BY id`, projectID, sessionID, partialID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var contID, errID int64
	var contContent, contReasoning, contUsage string
	for rows.Next() {
		var id int64
		var role, content string
		var reasoning, usage sql.NullString
		if err := rows.Scan(&id, &role, &content, &reasoning, &usage); err != nil {
			return 0, err
		}
		// The turn may have done tool rounds after the partial — the final
		// assistant message carries the continuation text; the last error
		// row is the one to drop.
		if role == "assistant" {
			contID = id
			contContent, contReasoning, contUsage = content, reasoning.String, usage.String
		} else if role == "error" {
			errID = id
		}
	}
	if contID == 0 {
		return 0, ErrNotFound
	}
	content := partial.Content
	if contContent != "" {
		content = strings.TrimRight(content, "\n") + "\n\n" + strings.TrimSpace(contContent)
	}
	reasoning := strings.TrimRight(partial.Reasoning, "\n")
	if contReasoning != "" {
		reasoning += "\n" + strings.TrimSpace(contReasoning)
	}
	usage := partial.Usage
	if usage == "" {
		usage = contUsage
	}
	if _, err := s.db.Exec(`UPDATE messages SET content = ?, reasoning = ?, usage = ? WHERE id = ?`,
		content, reasoning, usage, partialID); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`DELETE FROM messages WHERE id IN (?, ?)`, contID, errID); err != nil {
		return 0, err
	}
	return partialID, nil
}

// DeleteMessagesAfter removes every message with id greater than id within a
// chat session. Chat retry uses it to drop the aborted turn following the
// last user message.
func (s *Store) DeleteMessagesAfter(projectID, sessionID string, id int64) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE project_id = ? AND session_id = ? AND id > ?`, projectID, sessionID, id)
	return err
}

// ---- chat sessions ----

// ChatSession is one chat thread of a project.
type ChatSession struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	Archived  bool   `json:"archived"`
}

// ListSessions returns a project's chat sessions, oldest first.
func (s *Store) ListSessions(projectID string) ([]ChatSession, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at, archived FROM chat_sessions WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatSession{}
	for rows.Next() {
		var cs ChatSession
		var arch int
		if err := rows.Scan(&cs.ID, &cs.Name, &cs.CreatedAt, &arch); err != nil {
			return nil, err
		}
		cs.Archived = arch != 0
		out = append(out, cs)
	}
	return out, rows.Err()
}

// CreateChatSession adds a chat session. Empty names are auto-numbered
// ("Session 1", "Session 2", …).
func (s *Store) CreateChatSession(projectID, name string) (ChatSession, error) {
	if strings.TrimSpace(name) == "" {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_sessions WHERE project_id = ?`, projectID).Scan(&n); err != nil {
			return ChatSession{}, err
		}
		name = fmt.Sprintf("Session %d", n+1)
	}
	cs := ChatSession{ID: NewID(), Name: name, CreatedAt: now()}
	if _, err := s.db.Exec(`INSERT INTO chat_sessions (id, project_id, name, created_at) VALUES (?, ?, ?, ?)`,
		cs.ID, projectID, cs.Name, cs.CreatedAt); err != nil {
		return ChatSession{}, err
	}
	return cs, nil
}

// RenameChatSession renames one of the project's chat sessions.
func (s *Store) RenameChatSession(projectID, sessionID, name string) error {
	_, err := s.db.Exec(`UPDATE chat_sessions SET name = ? WHERE id = ? AND project_id = ?`, name, sessionID, projectID)
	return err
}

// ArchiveChatSession marks a chat session archived (hidden from the switcher
// but still present with its history).
func (s *Store) ArchiveChatSession(projectID, sessionID string) error {
	_, err := s.db.Exec(`UPDATE chat_sessions SET archived = 1 WHERE id = ? AND project_id = ?`, sessionID, projectID)
	return err
}

// UnarchiveChatSession restores an archived chat session to the active list.
func (s *Store) UnarchiveChatSession(projectID, sessionID string) error {
	_, err := s.db.Exec(`UPDATE chat_sessions SET archived = 0 WHERE id = ? AND project_id = ?`, sessionID, projectID)
	return err
}

// DeleteChatSession deletes a chat session and its messages from a project.
func (s *Store) DeleteChatSession(projectID, sessionID string) error {
	if _, err := s.db.Exec(`DELETE FROM messages WHERE project_id = ? AND session_id = ?`, projectID, sessionID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM chat_sessions WHERE id = ? AND project_id = ?`, sessionID, projectID)
	return err
}

// EnsureDefaultSession returns the project's first chat session, creating
// "Session 1" (and claiming any pre-session messages for it) when the project
// has none yet.
func (s *Store) EnsureDefaultSession(projectID string) (ChatSession, error) {
	sessions, err := s.ListSessions(projectID)
	if err != nil {
		return ChatSession{}, err
	}
	if len(sessions) > 0 {
		return sessions[0], nil
	}
	cs, err := s.CreateChatSession(projectID, "")
	if err != nil {
		return ChatSession{}, err
	}
	if _, err := s.db.Exec(`UPDATE messages SET session_id = ? WHERE project_id = ? AND session_id IS NULL`, cs.ID, projectID); err != nil {
		return ChatSession{}, err
	}
	return cs, nil
}

// Memory is one fact the agent saved for a project. Category is one of
// preference|episodic|fact|plan. Importance >= 2 pins the memory (never
// decays); lower importance fades 0.1 per day since last access, and a faded,
// long-unused memory is deleted lazily on the next read.
type Memory struct {
	ID           int64   `json:"id"`
	Content      string  `json:"content"`
	Enabled      bool    `json:"enabled"`
	CreatedAt    int64   `json:"createdAt"`
	Category     string  `json:"category"`
	Importance   float64 `json:"importance"`
	LastAccessed int64   `json:"lastAccessed"`
	AccessCount  int     `json:"accessCount"`
}

// AddMemory stores a memory for a project and returns its id.
func (s *Store) AddMemory(projectID, content, category string, importance float64) (int64, error) {
	if category == "" {
		category = "fact"
	}
	if importance <= 0 {
		importance = 1
	}
	res, err := s.db.Exec(`INSERT INTO memories (project_id, content, category, importance, created_at) VALUES (?, ?, ?, ?, ?)`,
		projectID, content, category, importance, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EffectiveImportance computes a memory's current weight: pinned memories
// (importance >= 2) never decay; everything else fades 0.1 per day since its
// last access (creation date when never accessed), floored at 0.
func EffectiveImportance(m Memory, at time.Time) float64 {
	if m.Importance >= 2 {
		return m.Importance
	}
	ref := time.Unix(m.LastAccessed, 0)
	if m.LastAccessed == 0 {
		ref = time.Unix(m.CreatedAt, 0)
	}
	days := at.Sub(ref).Hours() / 24
	eff := m.Importance - 0.1*days
	if eff < 0 {
		eff = 0
	}
	return eff
}

// ListMemories returns a project's memories oldest first, with decay applied:
// faded (effective importance < 0.2) memories unused for over 30 days are
// dropped lazily, and all returned entries carry their effective importance
// so ranking can use it without mutating.
func (s *Store) ListMemories(projectID string) ([]Memory, error) {
	rows, err := s.db.Query(`SELECT id, content, enabled, category, importance, last_accessed, access_count, created_at FROM memories WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var out []Memory
	var stale []int64
	for rows.Next() {
		var m Memory
		var enabled int
		if err := rows.Scan(&m.ID, &m.Content, &enabled, &m.Category, &m.Importance, &m.LastAccessed, &m.AccessCount, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled != 0
		m.Importance = EffectiveImportance(m, now)
		if m.Importance < 0.2 && m.LastAccessed > 0 && now.Sub(time.Unix(m.LastAccessed, 0)) > 30*24*time.Hour {
			stale = append(stale, m.ID)
			continue
		}
		out = append(out, m)
	}
	if len(stale) > 0 {
		s.db.Exec(`DELETE FROM memories WHERE id = ?`, stale[0])
		for _, id := range stale[1:] {
			s.db.Exec(`DELETE FROM memories WHERE id = ?`, id)
		}
	}
	return out, rows.Err()
}

// TouchMemory records a retrieval: bumps last_access and the access count so
// frequently-used memories rank higher over time.
func (s *Store) TouchMemory(id int64) error {
	_, err := s.db.Exec(`UPDATE memories SET last_accessed = ?, access_count = access_count + 1 WHERE id = ?`, now(), id)
	return err
}

// SetLastAccessed rewrites a memory's last_accessed timestamp (used by the
// decay tests to simulate disuse).
func (s *Store) SetLastAccessed(id int64, unix int64) error {
	_, err := s.db.Exec(`UPDATE memories SET last_accessed = ? WHERE id = ?`, unix, id)
	return err
}

// GetPlan returns the project's active plan JSON and whether one exists.
func (s *Store) GetPlan(projectID string) (string, bool, error) {
	var plan string
	err := s.db.QueryRow(`SELECT plan_json FROM plans WHERE project_id = ?`, projectID).Scan(&plan)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return plan, true, nil
}

// SetPlan upserts the project's active plan.
func (s *Store) SetPlan(projectID, planJSON string) error {
	_, err := s.db.Exec(`INSERT INTO plans (project_id, plan_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET plan_json = excluded.plan_json, updated_at = excluded.updated_at`,
		projectID, planJSON, now())
	return err
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
func (s *Store) LastUserMessage(projectID, sessionID string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT id, project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at
		FROM messages WHERE project_id = ? AND session_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`, projectID, sessionID))
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
func (s *Store) ListMessages(projectID, sessionID string) ([]*Message, error) {
	rows, err := s.db.Query(`SELECT id, project_id, role, content, tool_json, model, reasoning, usage, attachments, created_at
		FROM messages WHERE project_id = ? AND session_id = ? ORDER BY id ASC`, projectID, sessionID)
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
