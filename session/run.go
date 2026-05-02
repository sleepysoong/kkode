package session

import (
	"context"
	"time"
)

// Run은 gateway background 작업 상태를 SQLite에 영속화하는 레코드예요.
type Run struct {
	ID         string            `json:"id"`
	SessionID  string            `json:"session_id"`
	TurnID     string            `json:"turn_id,omitempty"`
	Status     string            `json:"status"`
	Prompt     string            `json:"prompt,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	MCPServers []string          `json:"mcp_servers,omitempty"`
	Skills     []string          `json:"skills,omitempty"`
	Subagents  []string          `json:"subagents,omitempty"`
	EventsURL  string            `json:"events_url,omitempty"`
	StartedAt  time.Time         `json:"started_at,omitempty"`
	EndedAt    time.Time         `json:"ended_at,omitempty"`
	Error      string            `json:"error,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type RunQuery struct {
	SessionID string
	Status    string
	Limit     int
}

// RunEvent는 run 상태 변경 snapshot을 durable replay용으로 남기는 레코드예요.
type RunEvent struct {
	ID    string    `json:"id"`
	RunID string    `json:"run_id"`
	Seq   int       `json:"seq"`
	Type  string    `json:"type"`
	At    time.Time `json:"at"`
	Run   Run       `json:"run"`
}

type RunEventQuery struct {
	RunID    string
	AfterSeq int
	Limit    int
}

type RunStore interface {
	SaveRun(ctx context.Context, run Run) (Run, error)
	LoadRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, q RunQuery) ([]Run, error)
}

type RunEventStore interface {
	AppendRunEvent(ctx context.Context, event RunEvent) (RunEvent, error)
	ListRunEvents(ctx context.Context, q RunEventQuery) ([]RunEvent, error)
}
