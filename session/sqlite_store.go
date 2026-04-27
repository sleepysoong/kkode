package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sleepysoong/kkode/llm"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) migrate(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			project_root TEXT NOT NULL,
			provider_name TEXT NOT NULL,
			model TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			mode TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			last_response_id TEXT NOT NULL DEFAULT '',
			last_input_items_json BLOB NOT NULL DEFAULT '[]',
			metadata_json BLOB NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS turns (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			ordinal INTEGER NOT NULL,
			prompt TEXT NOT NULL,
			request_json BLOB NOT NULL,
			response_json BLOB,
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_turns_session_ordinal ON turns(session_id, ordinal);`,
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			turn_id TEXT NOT NULL DEFAULT '',
			ordinal INTEGER NOT NULL,
			at TEXT NOT NULL,
			type TEXT NOT NULL,
			tool TEXT NOT NULL DEFAULT '',
			payload_json BLOB,
			error TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_events_session_ordinal ON events(session_id, ordinal);`,
		`CREATE TABLE IF NOT EXISTS todos (
			id TEXT NOT NULL,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			ordinal INTEGER NOT NULL,
			content TEXT NOT NULL,
			status TEXT NOT NULL,
			priority TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			PRIMARY KEY(session_id, id)
		);`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			turn_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			payload_json BLOB
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	if sess.ID == "" {
		sess.ID = NewID("sess")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sessions WHERE id = ?`, sess.ID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return fmt.Errorf("session already exists: %s", sess.ID)
	}
	return s.SaveSession(ctx, sess)
}

func (s *SQLiteStore) SaveSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	normalizeSession(sess)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lastItems, err := json.Marshal(sess.LastInputItems)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(sess.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions (
		id, project_root, provider_name, model, agent_name, mode, summary, last_response_id, last_input_items_json, metadata_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		project_root=excluded.project_root,
		provider_name=excluded.provider_name,
		model=excluded.model,
		agent_name=excluded.agent_name,
		mode=excluded.mode,
		summary=excluded.summary,
		last_response_id=excluded.last_response_id,
		last_input_items_json=excluded.last_input_items_json,
		metadata_json=excluded.metadata_json,
		updated_at=excluded.updated_at`,
		sess.ID, sess.ProjectRoot, sess.ProviderName, sess.Model, sess.AgentName, string(sess.Mode), sess.Summary, sess.LastResponseID, lastItems, metadata, formatTime(sess.CreatedAt), formatTime(sess.UpdatedAt))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM turns WHERE session_id = ?`, sess.ID); err != nil {
		return err
	}
	for i, turn := range sess.Turns {
		if turn.ID == "" {
			turn.ID = NewID("turn")
			sess.Turns[i].ID = turn.ID
		}
		req, err := json.Marshal(turn.Request)
		if err != nil {
			return err
		}
		var resp []byte
		if turn.Response != nil {
			resp, err = json.Marshal(turn.Response)
			if err != nil {
				return err
			}
		}
		if turn.StartedAt.IsZero() {
			turn.StartedAt = sess.CreatedAt
		}
		if turn.EndedAt.IsZero() {
			turn.EndedAt = turn.StartedAt
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO turns (id, session_id, ordinal, prompt, request_json, response_json, started_at, ended_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			turn.ID, sess.ID, i, turn.Prompt, req, nullableBytes(resp), formatTime(turn.StartedAt), formatTime(turn.EndedAt), turn.Error)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE session_id = ?`, sess.ID); err != nil {
		return err
	}
	for i, ev := range sess.Events {
		normalizeEvent(sess.ID, &ev)
		sess.Events[i] = ev
		_, err = tx.ExecContext(ctx, `INSERT INTO events (id, session_id, turn_id, ordinal, at, type, tool, payload_json, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID, ev.SessionID, ev.TurnID, i, formatTime(ev.At), ev.Type, ev.Tool, nullableBytes(ev.Payload), ev.Error)
		if err != nil {
			return err
		}
	}
	if err := replaceTodos(ctx, tx, sess.ID, sess.Todos); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, project_root, provider_name, model, agent_name, mode, summary, last_response_id, last_input_items_json, metadata_json, created_at, updated_at FROM sessions WHERE id = ?`, id)
	var sess Session
	var mode string
	var created, updated string
	var lastItems, metadata []byte
	if err := row.Scan(&sess.ID, &sess.ProjectRoot, &sess.ProviderName, &sess.Model, &sess.AgentName, &mode, &sess.Summary, &sess.LastResponseID, &lastItems, &metadata, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("session not found: %s", id)
		}
		return nil, err
	}
	sess.Mode = AgentMode(mode)
	sess.CreatedAt = parseTime(created)
	sess.UpdatedAt = parseTime(updated)
	if len(lastItems) > 0 {
		if err := json.Unmarshal(lastItems, &sess.LastInputItems); err != nil {
			return nil, err
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &sess.Metadata); err != nil {
			return nil, err
		}
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]string{}
	}
	turns, err := s.loadTurns(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.Turns = turns
	events, err := s.loadEvents(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.Events = events
	todos, err := s.loadTodos(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.Todos = todos
	return &sess, nil
}

func (s *SQLiteStore) ListSessions(ctx context.Context, q SessionQuery) ([]SessionSummary, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT s.id, s.project_root, s.provider_name, s.model, s.agent_name, s.mode, s.summary, s.updated_at, COUNT(t.id) AS turn_count
		FROM sessions s LEFT JOIN turns t ON t.session_id = s.id`
	args := []any{}
	if q.ProjectRoot != "" {
		query += ` WHERE s.project_root = ?`
		args = append(args, q.ProjectRoot)
	}
	query += ` GROUP BY s.id ORDER BY s.updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		var mode string
		var updated string
		if err := rows.Scan(&ss.ID, &ss.ProjectRoot, &ss.ProviderName, &ss.Model, &ss.AgentName, &mode, &ss.Summary, &updated, &ss.TurnCount); err != nil {
			return nil, err
		}
		ss.Mode = AgentMode(mode)
		ss.UpdatedAt = parseTime(updated)
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AppendEvent(ctx context.Context, ev Event) error {
	normalizeEvent(ev.SessionID, &ev)
	var ordinal int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) + 1 FROM events WHERE session_id = ?`, ev.SessionID).Scan(&ordinal); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (id, session_id, turn_id, ordinal, at, type, tool, payload_json, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.SessionID, ev.TurnID, ordinal, formatTime(ev.At), ev.Type, ev.Tool, nullableBytes(ev.Payload), ev.Error)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), ev.SessionID)
	return err
}

func (s *SQLiteStore) SaveCheckpoint(ctx context.Context, cp Checkpoint) error {
	if cp.ID == "" {
		cp.ID = NewID("cp")
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, turn_id, created_at, payload_json) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET payload_json=excluded.payload_json`, cp.ID, cp.SessionID, cp.TurnID, formatTime(cp.CreatedAt), nullableBytes(cp.Payload))
	return err
}

func (s *SQLiteStore) SaveTodos(ctx context.Context, sessionID string, todos []Todo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceTodos(ctx, tx, sessionID, todos); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) loadTurns(ctx context.Context, sessionID string) ([]Turn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, prompt, request_json, response_json, started_at, ended_at, error FROM turns WHERE session_id = ? ORDER BY ordinal`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []Turn
	for rows.Next() {
		var t Turn
		var req, resp []byte
		var started, ended string
		if err := rows.Scan(&t.ID, &t.Prompt, &req, &resp, &started, &ended, &t.Error); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(req, &t.Request); err != nil {
			return nil, err
		}
		if len(resp) > 0 {
			var r llm.Response
			if err := json.Unmarshal(resp, &r); err != nil {
				return nil, err
			}
			t.Response = &r
		}
		t.StartedAt = parseTime(started)
		t.EndedAt = parseTime(ended)
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

func (s *SQLiteStore) loadEvents(ctx context.Context, sessionID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, turn_id, at, type, tool, payload_json, error FROM events WHERE session_id = ? ORDER BY ordinal`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var ev Event
		var at string
		var payload []byte
		if err := rows.Scan(&ev.ID, &ev.TurnID, &at, &ev.Type, &ev.Tool, &payload, &ev.Error); err != nil {
			return nil, err
		}
		ev.SessionID = sessionID
		ev.At = parseTime(at)
		if len(payload) > 0 {
			ev.Payload = append([]byte(nil), payload...)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (s *SQLiteStore) loadTodos(ctx context.Context, sessionID string) ([]Todo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, content, status, priority, updated_at FROM todos WHERE session_id = ? ORDER BY ordinal`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todos []Todo
	for rows.Next() {
		var td Todo
		var status string
		var updated string
		if err := rows.Scan(&td.ID, &td.Content, &status, &td.Priority, &updated); err != nil {
			return nil, err
		}
		td.Status = TodoStatus(status)
		td.UpdatedAt = parseTime(updated)
		todos = append(todos, td)
	}
	return todos, rows.Err()
}

func replaceTodos(ctx context.Context, tx *sql.Tx, sessionID string, todos []Todo) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM todos WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for i, todo := range todos {
		normalizeTodo(&todo)
		_, err := tx.ExecContext(ctx, `INSERT INTO todos (id, session_id, ordinal, content, status, priority, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			todo.ID, sessionID, i, todo.Content, string(todo.Status), todo.Priority, formatTime(todo.UpdatedAt))
		if err != nil {
			return err
		}
	}
	return nil
}

func normalizeSession(sess *Session) {
	now := time.Now().UTC()
	if sess.ID == "" {
		sess.ID = NewID("sess")
	}
	if sess.Mode == "" {
		sess.Mode = AgentModeBuild
	}
	if sess.AgentName == "" {
		sess.AgentName = "kkode-agent"
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]string{}
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = now
	}
}

func normalizeEvent(sessionID string, ev *Event) {
	if ev.ID == "" {
		ev.ID = NewID("ev")
	}
	if ev.SessionID == "" {
		ev.SessionID = sessionID
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
}

func normalizeTodo(todo *Todo) {
	if todo.ID == "" {
		todo.ID = NewID("todo")
	}
	if todo.Status == "" {
		todo.Status = TodoPending
	}
	if todo.UpdatedAt.IsZero() {
		todo.UpdatedAt = time.Now().UTC()
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
