package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func (s *SQLiteStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("sqlite store is not open")
	}
	return s.db.PingContext(ctx)
}

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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_turns_session_ordinal_unique ON turns(session_id, ordinal);`,
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
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_session_ordinal_unique ON events(session_id, ordinal);`,
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			turn_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			events_url TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			ended_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			metadata_json BLOB NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`ALTER TABLE runs ADD COLUMN provider TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE runs ADD COLUMN model TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE runs ADD COLUMN mcp_servers_json BLOB NOT NULL DEFAULT '[]';`,
		`ALTER TABLE runs ADD COLUMN skills_json BLOB NOT NULL DEFAULT '[]';`,
		`ALTER TABLE runs ADD COLUMN subagents_json BLOB NOT NULL DEFAULT '[]';`,
		`CREATE INDEX IF NOT EXISTS idx_runs_session_updated ON runs(session_id, updated_at);`,
		`CREATE INDEX IF NOT EXISTS idx_runs_status_updated ON runs(status, updated_at);`,
		`CREATE INDEX IF NOT EXISTS idx_runs_request_id_updated ON runs(json_extract(CAST(metadata_json AS TEXT), '$.request_id'), updated_at);`,
		`CREATE INDEX IF NOT EXISTS idx_runs_idempotency_key_updated ON runs(json_extract(CAST(metadata_json AS TEXT), '$.idempotency_key'), updated_at);`,
		`CREATE TABLE IF NOT EXISTS run_events (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL,
			at TEXT NOT NULL,
			type TEXT NOT NULL,
			run_json BLOB NOT NULL,
			UNIQUE(run_id, seq)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run_seq ON run_events(run_id, seq);`,
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
		`CREATE TABLE IF NOT EXISTS resources (
			id TEXT NOT NULL,
			kind TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			config_json BLOB NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(kind, id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_resources_kind_updated ON resources(kind, updated_at);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			if isIgnorableMigrationError(stmt, err) {
				continue
			}
			return err
		}
	}
	return nil
}

func isIgnorableMigrationError(stmt string, err error) bool {
	return strings.HasPrefix(strings.TrimSpace(stmt), "ALTER TABLE") && strings.Contains(err.Error(), "duplicate column name")
}

const sqliteSequenceRetryLimit = 4

func retrySQLiteSequence(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < sqliteSequenceRetryLimit; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		err = fn()
		if !isSQLiteUniqueConstraintError(err) {
			return err
		}
	}
	return err
}

func isSQLiteUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
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

func (s *SQLiteStore) AppendTurnWithEvents(ctx context.Context, sess *Session, turn Turn, events []Event) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	normalizeSession(sess)
	if turn.ID == "" {
		turn.ID = NewID("turn")
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now().UTC()
	}
	if turn.EndedAt.IsZero() {
		turn.EndedAt = turn.StartedAt
	}
	turnReq, err := json.Marshal(turn.Request)
	if err != nil {
		return err
	}
	var turnResp []byte
	if turn.Response != nil {
		turnResp, err = json.Marshal(turn.Response)
		if err != nil {
			return err
		}
	}
	lastItems, err := json.Marshal(sess.LastInputItems)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(sess.Metadata)
	if err != nil {
		return err
	}
	return retrySQLiteSequence(ctx, func() error {
		now := time.Now().UTC()
		sess.UpdatedAt = now
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var turnOrdinal int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) + 1 FROM turns WHERE session_id = ?`, sess.ID).Scan(&turnOrdinal); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO turns (id, session_id, ordinal, prompt, request_json, response_json, started_at, ended_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			turn.ID, sess.ID, turnOrdinal, turn.Prompt, turnReq, nullableBytes(turnResp), formatTime(turn.StartedAt), formatTime(turn.EndedAt), turn.Error); err != nil {
			return err
		}
		var eventOrdinal int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) + 1 FROM events WHERE session_id = ?`, sess.ID).Scan(&eventOrdinal); err != nil {
			return err
		}
		for i, ev := range events {
			normalizeEvent(sess.ID, &ev)
			if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, session_id, turn_id, ordinal, at, type, tool, payload_json, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ev.ID, ev.SessionID, ev.TurnID, eventOrdinal+i, formatTime(ev.At), ev.Type, ev.Tool, nullableBytes(ev.Payload), ev.Error); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET
		project_root = ?,
		provider_name = ?,
		model = ?,
		agent_name = ?,
		mode = ?,
		summary = ?,
		last_response_id = ?,
		last_input_items_json = ?,
		metadata_json = ?,
		updated_at = ?
		WHERE id = ?`,
			sess.ProjectRoot, sess.ProviderName, sess.Model, sess.AgentName, string(sess.Mode), sess.Summary, sess.LastResponseID, lastItems, metadata, formatTime(now), sess.ID)
		if err != nil {
			return err
		}
		if rows, err := res.RowsAffected(); err == nil && rows == 0 {
			return fmt.Errorf("session not found: %s", sess.ID)
		}
		return tx.Commit()
	})
}

func (s *SQLiteStore) SaveSessionState(ctx context.Context, sess *Session) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	normalizeSession(sess)
	lastItems, err := json.Marshal(sess.LastInputItems)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(sess.Metadata)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sess.UpdatedAt = now
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET
		project_root = ?,
		provider_name = ?,
		model = ?,
		agent_name = ?,
		mode = ?,
		summary = ?,
		last_response_id = ?,
		last_input_items_json = ?,
		metadata_json = ?,
		updated_at = ?
		WHERE id = ?`,
		sess.ProjectRoot, sess.ProviderName, sess.Model, sess.AgentName, string(sess.Mode), sess.Summary, sess.LastResponseID, lastItems, metadata, formatTime(now), sess.ID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return fmt.Errorf("session not found: %s", sess.ID)
	}
	return nil
}

func (s *SQLiteStore) AppendTurn(ctx context.Context, sessionID string, turn Turn) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	if turn.ID == "" {
		turn.ID = NewID("turn")
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now().UTC()
	}
	if turn.EndedAt.IsZero() {
		turn.EndedAt = turn.StartedAt
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
	return retrySQLiteSequence(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var ordinal int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) + 1 FROM turns WHERE session_id = ?`, sessionID).Scan(&ordinal); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO turns (id, session_id, ordinal, prompt, request_json, response_json, started_at, ended_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			turn.ID, sessionID, ordinal, turn.Prompt, req, nullableBytes(resp), formatTime(turn.StartedAt), formatTime(turn.EndedAt), turn.Error)
		if err != nil {
			return err
		}
		if rows, err := res.RowsAffected(); err == nil && rows == 0 {
			return fmt.Errorf("turn append failed: %s", turn.ID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), sessionID); err != nil {
			return err
		}
		return tx.Commit()
	})
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

func (s *SQLiteStore) LoadStats(ctx context.Context) (StoreStats, error) {
	stats := StoreStats{Runs: map[string]int{}, Resources: map[string]int{}}
	counts := []struct {
		query string
		out   *int
	}{
		{query: `SELECT COUNT(1) FROM sessions`, out: &stats.Sessions},
		{query: `SELECT COUNT(1) FROM turns`, out: &stats.Turns},
		{query: `SELECT COUNT(1) FROM events`, out: &stats.Events},
		{query: `SELECT COUNT(1) FROM todos`, out: &stats.Todos},
		{query: `SELECT COUNT(1) FROM checkpoints`, out: &stats.Checkpoints},
	}
	for _, item := range counts {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.out); err != nil {
			return stats, err
		}
	}
	if err := scanGroupedCounts(ctx, s.db, `SELECT status, COUNT(1) FROM runs GROUP BY status`, stats.Runs); err != nil {
		return stats, err
	}
	if err := scanGroupedCounts(ctx, s.db, `SELECT kind, COUNT(1) FROM resources GROUP BY kind`, stats.Resources); err != nil {
		return stats, err
	}
	return stats, nil
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
	return retrySQLiteSequence(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var ordinal int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), -1) + 1 FROM events WHERE session_id = ?`, ev.SessionID).Scan(&ordinal); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, session_id, turn_id, ordinal, at, type, tool, payload_json, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID, ev.SessionID, ev.TurnID, ordinal, formatTime(ev.At), ev.Type, ev.Tool, nullableBytes(ev.Payload), ev.Error); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), ev.SessionID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *SQLiteStore) ListEvents(ctx context.Context, q EventQuery) ([]EventRecord, error) {
	if strings.TrimSpace(q.SessionID) == "" {
		return nil, errors.New("session id is required")
	}
	if err := s.requireSession(ctx, q.SessionID); err != nil {
		return nil, err
	}
	afterOrdinal := q.AfterSeq
	if afterOrdinal < 0 {
		afterOrdinal = 0
	}
	query := `SELECT ordinal, id, turn_id, at, type, tool, payload_json, error FROM events WHERE session_id = ? AND ordinal >= ? ORDER BY ordinal`
	args := []any{q.SessionID, afterOrdinal}
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		record, err := scanEventRecord(rows, q.SessionID)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListTurns(ctx context.Context, q TurnQuery) ([]TurnRecord, error) {
	if strings.TrimSpace(q.SessionID) == "" {
		return nil, errors.New("session id is required")
	}
	if err := s.requireSession(ctx, q.SessionID); err != nil {
		return nil, err
	}
	afterOrdinal := q.AfterSeq
	if afterOrdinal < 0 {
		afterOrdinal = 0
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal, id, prompt, request_json, response_json, started_at, ended_at, error FROM turns WHERE session_id = ? AND ordinal >= ? ORDER BY ordinal LIMIT ?`, q.SessionID, afterOrdinal, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnRecord
	for rows.Next() {
		record, err := scanTurnRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LoadTurn(ctx context.Context, sessionID string, turnID string) (TurnRecord, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" {
		return TurnRecord{}, errors.New("session id and turn id are required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT ordinal, id, prompt, request_json, response_json, started_at, ended_at, error FROM turns WHERE session_id = ? AND id = ?`, sessionID, turnID)
	record, err := scanTurnRecord(row)
	if err == nil {
		return record, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		if existsErr := s.requireSession(ctx, sessionID); existsErr != nil {
			return TurnRecord{}, existsErr
		}
		return TurnRecord{}, fmt.Errorf("turn not found: %s", turnID)
	}
	return TurnRecord{}, err
}

func (s *SQLiteStore) requireSession(ctx context.Context, sessionID string) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		return err
	}
	return nil
}

func (s *SQLiteStore) SaveCheckpoint(ctx context.Context, cp Checkpoint) error {
	normalizeCheckpoint(&cp)
	_, err := s.db.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, turn_id, created_at, payload_json) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET turn_id=excluded.turn_id, created_at=excluded.created_at, payload_json=excluded.payload_json`, cp.ID, cp.SessionID, cp.TurnID, formatTime(cp.CreatedAt), nullableBytes(cp.Payload))
	return err
}

func (s *SQLiteStore) LoadCheckpoint(ctx context.Context, sessionID string, checkpointID string) (Checkpoint, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, session_id, turn_id, created_at, payload_json FROM checkpoints WHERE session_id = ? AND id = ?`, sessionID, checkpointID)
	return scanCheckpoint(row)
}

func (s *SQLiteStore) ListCheckpoints(ctx context.Context, q CheckpointQuery) ([]Checkpoint, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, turn_id, created_at, payload_json FROM checkpoints WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`, q.SessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

type checkpointScanner interface {
	Scan(dest ...any) error
}

func scanCheckpoint(scanner checkpointScanner) (Checkpoint, error) {
	var cp Checkpoint
	var created string
	var payload []byte
	if err := scanner.Scan(&cp.ID, &cp.SessionID, &cp.TurnID, &created, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Checkpoint{}, fmt.Errorf("checkpoint not found")
		}
		return Checkpoint{}, err
	}
	cp.CreatedAt = parseTime(created)
	if len(payload) > 0 {
		cp.Payload = append([]byte(nil), payload...)
	}
	return cp, nil
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

func (s *SQLiteStore) SaveRun(ctx context.Context, run Run) (Run, error) {
	normalizeRun(&run)
	if err := saveRun(ctx, s.db, run); err != nil {
		return run, err
	}
	return run, nil
}

// SaveRunWithEvent는 run snapshot과 durable replay event를 같은 SQLite transaction에 저장해요.
// gateway 재시작이나 adapter replay가 같은 상태 전이를 보도록 SaveRun+AppendRunEvent 분리보다 이 경로를 우선 써야 해요.
func (s *SQLiteStore) SaveRunWithEvent(ctx context.Context, run Run, event RunEvent) (Run, RunEvent, error) {
	normalizeRun(&run)
	event.Run = run
	normalizeRunEvent(&event)
	var savedEvent RunEvent
	err := retrySQLiteSequence(ctx, func() error {
		candidate := event
		candidate.Seq = event.Seq
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := saveRun(ctx, tx, run); err != nil {
			return err
		}
		if err := appendRunEvent(ctx, tx, &candidate); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		savedEvent = candidate
		return nil
	})
	if err != nil {
		return run, event, err
	}
	return run, savedEvent, nil
}

// ClaimRunWithEvent는 run id가 비어 있을 때만 INSERT하고 queued event를 같은 transaction에 남겨요.
// 이미 같은 id가 있으면 기존 run을 반환하고 claimed=false를 돌려줘요.
func (s *SQLiteStore) ClaimRunWithEvent(ctx context.Context, run Run, event RunEvent) (Run, RunEvent, bool, error) {
	normalizeRun(&run)
	event.Run = run
	normalizeRunEvent(&event)
	var savedRun Run
	var savedEvent RunEvent
	claimed := false
	err := retrySQLiteSequence(ctx, func() error {
		candidate := event
		candidate.Seq = event.Seq
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		inserted, err := insertRunIfAbsent(ctx, tx, run)
		if err != nil {
			return err
		}
		if !inserted {
			existing, err := loadRunWithWriter(ctx, tx, run.ID)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			savedRun = existing
			savedEvent = RunEvent{}
			claimed = false
			return nil
		}
		if err := appendRunEvent(ctx, tx, &candidate); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		savedRun = run
		savedEvent = candidate
		claimed = true
		return nil
	})
	if err != nil {
		return run, event, false, err
	}
	return savedRun, savedEvent, claimed, nil
}

type runWriter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type runLoader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type runEventWriter interface {
	runWriter
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func insertRunIfAbsent(ctx context.Context, writer runWriter, run Run) (bool, error) {
	metadata, err := json.Marshal(run.Metadata)
	if err != nil {
		return false, err
	}
	mcpServers, err := json.Marshal(run.MCPServers)
	if err != nil {
		return false, err
	}
	skills, err := json.Marshal(run.Skills)
	if err != nil {
		return false, err
	}
	subagents, err := json.Marshal(run.Subagents)
	if err != nil {
		return false, err
	}
	result, err := writer.ExecContext(ctx, `INSERT INTO runs (id, session_id, turn_id, status, prompt, provider, model, mcp_servers_json, skills_json, subagents_json, events_url, started_at, ended_at, error, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		run.ID, run.SessionID, run.TurnID, run.Status, run.Prompt, run.Provider, run.Model, mcpServers, skills, subagents, run.EventsURL, formatOptionalTime(run.StartedAt), formatOptionalTime(run.EndedAt), run.Error, metadata, formatTime(run.CreatedAt), formatTime(run.UpdatedAt))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func saveRun(ctx context.Context, writer runWriter, run Run) error {
	metadata, err := json.Marshal(run.Metadata)
	if err != nil {
		return err
	}
	mcpServers, err := json.Marshal(run.MCPServers)
	if err != nil {
		return err
	}
	skills, err := json.Marshal(run.Skills)
	if err != nil {
		return err
	}
	subagents, err := json.Marshal(run.Subagents)
	if err != nil {
		return err
	}
	_, err = writer.ExecContext(ctx, `INSERT INTO runs (id, session_id, turn_id, status, prompt, provider, model, mcp_servers_json, skills_json, subagents_json, events_url, started_at, ended_at, error, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id=excluded.session_id,
			turn_id=excluded.turn_id,
			status=excluded.status,
			prompt=excluded.prompt,
			provider=excluded.provider,
			model=excluded.model,
			mcp_servers_json=excluded.mcp_servers_json,
			skills_json=excluded.skills_json,
			subagents_json=excluded.subagents_json,
			events_url=excluded.events_url,
			started_at=excluded.started_at,
			ended_at=excluded.ended_at,
			error=excluded.error,
			metadata_json=excluded.metadata_json,
			updated_at=excluded.updated_at`,
		run.ID, run.SessionID, run.TurnID, run.Status, run.Prompt, run.Provider, run.Model, mcpServers, skills, subagents, run.EventsURL, formatOptionalTime(run.StartedAt), formatOptionalTime(run.EndedAt), run.Error, metadata, formatTime(run.CreatedAt), formatTime(run.UpdatedAt))
	return err
}

func (s *SQLiteStore) LoadRun(ctx context.Context, id string) (Run, error) {
	return loadRunWithWriter(ctx, s.db, id)
}

func loadRunWithWriter(ctx context.Context, reader runLoader, id string) (Run, error) {
	row := reader.QueryRowContext(ctx, `SELECT id, session_id, turn_id, status, prompt, provider, model, mcp_servers_json, skills_json, subagents_json, events_url, started_at, ended_at, error, metadata_json, created_at, updated_at FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *SQLiteStore) ListRuns(ctx context.Context, q RunQuery) ([]Run, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, session_id, turn_id, status, prompt, provider, model, mcp_servers_json, skills_json, subagents_json, events_url, started_at, ended_at, error, metadata_json, created_at, updated_at FROM runs`
	args := []any{}
	where := []string{}
	if q.SessionID != "" {
		where = append(where, `session_id = ?`)
		args = append(args, q.SessionID)
	}
	if q.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, q.Status)
	}
	if q.RequestID != "" {
		where = append(where, `json_extract(CAST(metadata_json AS TEXT), '$.request_id') = ?`)
		args = append(args, q.RequestID)
	}
	if q.IdempotencyKey != "" {
		where = append(where, `json_extract(CAST(metadata_json AS TEXT), '$.idempotency_key') = ?`)
		args = append(args, q.IdempotencyKey)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AppendRunEvent(ctx context.Context, event RunEvent) (RunEvent, error) {
	normalizeRunEvent(&event)
	var saved RunEvent
	err := retrySQLiteSequence(ctx, func() error {
		candidate := event
		candidate.Seq = event.Seq
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := appendRunEvent(ctx, tx, &candidate); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		saved = candidate
		return nil
	})
	if err != nil {
		return event, err
	}
	return saved, nil
}

func appendRunEvent(ctx context.Context, writer runEventWriter, event *RunEvent) error {
	runJSON, err := json.Marshal(event.Run)
	if err != nil {
		return err
	}
	if event.Seq <= 0 {
		if err := writer.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, event.RunID).Scan(&event.Seq); err != nil {
			return err
		}
	}
	_, err = writer.ExecContext(ctx, `INSERT INTO run_events (id, run_id, seq, at, type, run_json) VALUES (?, ?, ?, ?, ?, ?)`, event.ID, event.RunID, event.Seq, formatTime(event.At), event.Type, runJSON)
	return err
}

func (s *SQLiteStore) ListRunEvents(ctx context.Context, q RunEventQuery) ([]RunEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, run_id, seq, at, type, run_json FROM run_events WHERE run_id = ?`
	args := []any{q.RunID}
	if q.AfterSeq > 0 {
		query += ` AND seq > ?`
		args = append(args, q.AfterSeq)
	}
	query += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		event, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

type runEventScanner interface {
	Scan(dest ...any) error
}

func scanRunEvent(scanner runEventScanner) (RunEvent, error) {
	var event RunEvent
	var at string
	var runJSON []byte
	if err := scanner.Scan(&event.ID, &event.RunID, &event.Seq, &at, &event.Type, &runJSON); err != nil {
		return RunEvent{}, err
	}
	event.At = parseTime(at)
	if len(runJSON) > 0 {
		if err := json.Unmarshal(runJSON, &event.Run); err != nil {
			return RunEvent{}, err
		}
	}
	return event, nil
}

func normalizeRunEvent(event *RunEvent) {
	if event.ID == "" {
		event.ID = NewID("runev")
	}
	if event.RunID == "" {
		event.RunID = event.Run.ID
	}
	if event.Type == "" {
		event.Type = "run." + event.Run.Status
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
}

type runScanner interface {
	Scan(dest ...any) error
}

func scanRun(scanner runScanner) (Run, error) {
	var run Run
	var started, ended, created, updated string
	var metadata, mcpServers, skills, subagents []byte
	if err := scanner.Scan(&run.ID, &run.SessionID, &run.TurnID, &run.Status, &run.Prompt, &run.Provider, &run.Model, &mcpServers, &skills, &subagents, &run.EventsURL, &started, &ended, &run.Error, &metadata, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("run not found")
		}
		return Run{}, err
	}
	run.StartedAt = parseOptionalTime(started)
	run.EndedAt = parseOptionalTime(ended)
	run.CreatedAt = parseTime(created)
	run.UpdatedAt = parseTime(updated)
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &run.Metadata); err != nil {
			return Run{}, err
		}
	}
	if len(mcpServers) > 0 {
		if err := json.Unmarshal(mcpServers, &run.MCPServers); err != nil {
			return Run{}, err
		}
	}
	if len(skills) > 0 {
		if err := json.Unmarshal(skills, &run.Skills); err != nil {
			return Run{}, err
		}
	}
	if len(subagents) > 0 {
		if err := json.Unmarshal(subagents, &run.Subagents); err != nil {
			return Run{}, err
		}
	}
	if run.Metadata == nil {
		run.Metadata = map[string]string{}
	}
	return run, nil
}

func normalizeRun(run *Run) {
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = NewID("run")
	}
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.Metadata == nil {
		run.Metadata = map[string]string{}
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	return parseTime(value)
}

func (s *SQLiteStore) SaveResource(ctx context.Context, resource Resource) (Resource, error) {
	normalizeResource(&resource)
	_, err := s.db.ExecContext(ctx, `INSERT INTO resources (id, kind, name, description, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, id) DO UPDATE SET
			name=excluded.name,
			description=excluded.description,
			enabled=excluded.enabled,
			config_json=excluded.config_json,
			updated_at=excluded.updated_at`,
		resource.ID, string(resource.Kind), resource.Name, resource.Description, boolInt(resource.Enabled), nullableBytes(resource.Config), formatTime(resource.CreatedAt), formatTime(resource.UpdatedAt))
	return resource, err
}

func (s *SQLiteStore) LoadResource(ctx context.Context, kind ResourceKind, id string) (Resource, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, kind, name, description, enabled, config_json, created_at, updated_at FROM resources WHERE kind = ? AND id = ?`, string(kind), id)
	return scanResource(row)
}

func (s *SQLiteStore) ListResources(ctx context.Context, q ResourceQuery) ([]Resource, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, kind, name, description, enabled, config_json, created_at, updated_at FROM resources`
	args := []any{}
	where := []string{}
	if q.Kind != "" {
		where = append(where, `kind = ?`)
		args = append(args, string(q.Kind))
	}
	if q.Enabled != nil {
		where = append(where, `enabled = ?`)
		args = append(args, boolInt(*q.Enabled))
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Resource
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, resource)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteResource(ctx context.Context, kind ResourceKind, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE kind = ? AND id = ?`, string(kind), id)
	if err != nil {
		return err
	}
	if count, err := res.RowsAffected(); err == nil && count == 0 {
		return fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	return nil
}

type resourceScanner interface {
	Scan(dest ...any) error
}

func scanResource(scanner resourceScanner) (Resource, error) {
	var resource Resource
	var kind string
	var enabled int
	var config []byte
	var created, updated string
	if err := scanner.Scan(&resource.ID, &kind, &resource.Name, &resource.Description, &enabled, &config, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Resource{}, fmt.Errorf("resource not found")
		}
		return Resource{}, err
	}
	resource.Kind = ResourceKind(kind)
	resource.Enabled = enabled != 0
	if len(config) > 0 {
		resource.Config = append([]byte(nil), config...)
	}
	resource.CreatedAt = parseTime(created)
	resource.UpdatedAt = parseTime(updated)
	return resource, nil
}

func normalizeResource(resource *Resource) {
	now := time.Now().UTC()
	if resource.ID == "" {
		resource.ID = NewID(resourcePrefix(resource.Kind))
	}
	if len(resource.Config) == 0 {
		resource.Config = json.RawMessage(`{}`)
	}
	if resource.CreatedAt.IsZero() {
		resource.CreatedAt = now
	}
	resource.UpdatedAt = now
}

func resourcePrefix(kind ResourceKind) string {
	switch kind {
	case ResourceMCPServer:
		return "mcp"
	case ResourceSkill:
		return "skill"
	case ResourceSubagent:
		return "subagent"
	default:
		return "resource"
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func scanTurnRecord(scanner interface{ Scan(dest ...any) error }) (TurnRecord, error) {
	var record TurnRecord
	var req, resp []byte
	var started, ended string
	ordinal := 0
	if err := scanner.Scan(&ordinal, &record.Turn.ID, &record.Turn.Prompt, &req, &resp, &started, &ended, &record.Turn.Error); err != nil {
		return TurnRecord{}, err
	}
	record.Seq = ordinal + 1
	if err := json.Unmarshal(req, &record.Turn.Request); err != nil {
		return TurnRecord{}, err
	}
	if len(resp) > 0 {
		var response llm.Response
		if err := json.Unmarshal(resp, &response); err != nil {
			return TurnRecord{}, err
		}
		record.Turn.Response = &response
	}
	record.Turn.StartedAt = parseTime(started)
	record.Turn.EndedAt = parseTime(ended)
	return record, nil
}

func scanEventRecord(scanner interface{ Scan(dest ...any) error }, sessionID string) (EventRecord, error) {
	var record EventRecord
	var at string
	var payload []byte
	ordinal := 0
	if err := scanner.Scan(&ordinal, &record.Event.ID, &record.Event.TurnID, &at, &record.Event.Type, &record.Event.Tool, &payload, &record.Event.Error); err != nil {
		return EventRecord{}, err
	}
	record.Seq = ordinal + 1
	record.Event.SessionID = sessionID
	record.Event.At = parseTime(at)
	if len(payload) > 0 {
		record.Event.Payload = append([]byte(nil), payload...)
	}
	return record, nil
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

func scanGroupedCounts(ctx context.Context, db *sql.DB, query string, out map[string]int) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return err
		}
		out[key] = count
	}
	return rows.Err()
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

func normalizeCheckpoint(cp *Checkpoint) {
	if cp.ID == "" {
		cp.ID = NewID("cp")
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	if len(cp.Payload) == 0 {
		cp.Payload = json.RawMessage(`{}`)
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
