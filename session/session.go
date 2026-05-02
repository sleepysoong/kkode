package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

type AgentMode string

const (
	AgentModeBuild AgentMode = "build"
	AgentModePlan  AgentMode = "plan"
	AgentModeAsk   AgentMode = "ask"
)

type Session struct {
	ID             string            `json:"id"`
	ProjectRoot    string            `json:"project_root"`
	ProviderName   string            `json:"provider_name"`
	Model          string            `json:"model"`
	AgentName      string            `json:"agent_name"`
	Mode           AgentMode         `json:"mode"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Turns          []Turn            `json:"turns"`
	Events         []Event           `json:"events"`
	Todos          []Todo            `json:"todos"`
	Summary        string            `json:"summary"`
	LastResponseID string            `json:"last_response_id"`
	LastInputItems []llm.Item        `json:"last_input_items"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type Turn struct {
	ID        string        `json:"id"`
	Prompt    string        `json:"prompt"`
	Request   llm.Request   `json:"request"`
	Response  *llm.Response `json:"response,omitempty"`
	StartedAt time.Time     `json:"started_at"`
	EndedAt   time.Time     `json:"ended_at"`
	Error     string        `json:"error,omitempty"`
}

type Event struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	At        time.Time       `json:"at"`
	Type      string          `json:"type"`
	Tool      string          `json:"tool,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

type Todo struct {
	ID        string     `json:"id"`
	Content   string     `json:"content"`
	Status    TodoStatus `json:"status"`
	Priority  string     `json:"priority,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Checkpoint struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type SessionQuery struct {
	ProjectRoot string
	Limit       int
}

type CheckpointQuery struct {
	SessionID string
	Limit     int
}

type SessionSummary struct {
	ID           string    `json:"id"`
	ProjectRoot  string    `json:"project_root"`
	ProviderName string    `json:"provider_name"`
	Model        string    `json:"model"`
	AgentName    string    `json:"agent_name"`
	Mode         AgentMode `json:"mode"`
	TurnCount    int       `json:"turn_count"`
	UpdatedAt    time.Time `json:"updated_at"`
	Summary      string    `json:"summary,omitempty"`
}

type Store interface {
	CreateSession(ctx context.Context, s *Session) error
	LoadSession(ctx context.Context, id string) (*Session, error)
	SaveSession(ctx context.Context, s *Session) error
	ListSessions(ctx context.Context, q SessionQuery) ([]SessionSummary, error)
	AppendEvent(ctx context.Context, ev Event) error
	SaveCheckpoint(ctx context.Context, cp Checkpoint) error
	Close() error
}

type CheckpointStore interface {
	SaveCheckpoint(ctx context.Context, cp Checkpoint) error
	LoadCheckpoint(ctx context.Context, sessionID string, checkpointID string) (Checkpoint, error)
	ListCheckpoints(ctx context.Context, q CheckpointQuery) ([]Checkpoint, error)
}

func NewSession(projectRoot, providerName, model, agentName string, mode AgentMode) *Session {
	now := time.Now().UTC()
	if mode == "" {
		mode = AgentModeBuild
	}
	if agentName == "" {
		agentName = "kkode-agent"
	}
	return &Session{
		ID:           NewID("sess"),
		ProjectRoot:  projectRoot,
		ProviderName: providerName,
		Model:        model,
		AgentName:    agentName,
		Mode:         mode,
		CreatedAt:    now,
		UpdatedAt:    now,
		Metadata:     map[string]string{},
	}
}

func NewTurn(prompt string, req llm.Request) Turn {
	now := time.Now().UTC()
	return Turn{ID: NewID("turn"), Prompt: prompt, Request: req, StartedAt: now}
}

func NewEvent(sessionID, turnID string, typ string) Event {
	return Event{ID: NewID("ev"), SessionID: sessionID, TurnID: turnID, Type: typ, At: time.Now().UTC()}
}

func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (s *Session) Touch() {
	s.UpdatedAt = time.Now().UTC()
}

func (s *Session) AppendTurn(turn Turn) {
	s.Turns = append(s.Turns, turn)
	if turn.Response != nil {
		s.LastResponseID = turn.Response.ID
		s.LastInputItems = append([]llm.Item{}, turn.Response.Output...)
	}
	s.Touch()
}

func (s *Session) AppendEvent(ev Event) {
	if ev.ID == "" {
		ev.ID = NewID("ev")
	}
	if ev.SessionID == "" {
		ev.SessionID = s.ID
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	s.Events = append(s.Events, ev)
	s.Touch()
}
