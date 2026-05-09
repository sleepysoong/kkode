package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

// Run은 gateway background 작업 상태를 SQLite에 영속화하는 레코드예요.
type Run struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id,omitempty"`
	Status    string `json:"status"`
	Prompt    string `json:"prompt,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	// WorkingDirectory는 project root 기준 subdir scoped instruction 힌트예요.
	WorkingDirectory string   `json:"working_directory,omitempty"`
	MaxOutputTokens  int      `json:"max_output_tokens,omitempty"`
	MCPServers       []string `json:"mcp_servers,omitempty"`
	Skills           []string `json:"skills,omitempty"`
	Subagents        []string `json:"subagents,omitempty"`
	// EnabledTools/DisabledTools는 run 당시 local tool surface 선택이에요.
	EnabledTools  []string `json:"enabled_tools,omitempty"`
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// ContextBlocks는 저장된 resource가 아닌 요청 단위 임시 prompt context예요.
	ContextBlocks []string          `json:"context_blocks,omitempty"`
	EventsURL     string            `json:"events_url,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	EndedAt       time.Time         `json:"ended_at,omitempty"`
	Error         string            `json:"error,omitempty"`
	Usage         llm.Usage         `json:"usage,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type RunQuery struct {
	SessionID      string
	TurnID         string
	Status         string
	Provider       string
	Model          string
	RequestID      string
	IdempotencyKey string
	Limit          int
	Offset         int
}

// RunEvent는 run 상태 변경 snapshot을 durable replay용으로 남기는 레코드예요.
type RunEvent struct {
	ID      string          `json:"id"`
	RunID   string          `json:"run_id"`
	Seq     int             `json:"seq"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Tool    string          `json:"tool,omitempty"`
	Message string          `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Run     Run             `json:"run"`
}

type RunEventQuery struct {
	RunID    string
	AfterSeq int
	Type     string
	Limit    int
}

type RunStore interface {
	SaveRun(ctx context.Context, run Run) (Run, error)
	LoadRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, q RunQuery) ([]Run, error)
}

type RunCounter interface {
	CountRuns(ctx context.Context, q RunQuery) (int, error)
}

type RunEventStore interface {
	AppendRunEvent(ctx context.Context, event RunEvent) (RunEvent, error)
	ListRunEvents(ctx context.Context, q RunEventQuery) ([]RunEvent, error)
}

// RunSnapshotStore는 run 상태와 replay event를 한 transaction으로 남기는 저장소예요.
// gateway는 이 interface가 있으면 SaveRun 뒤 AppendRunEvent가 갈라지는 경로 대신 원자 저장을 써요.
type RunSnapshotStore interface {
	SaveRunWithEvent(ctx context.Context, run Run, event RunEvent) (Run, RunEvent, error)
}

// RunClaimStore는 run id가 아직 없을 때만 queued snapshot을 삽입해요.
// 이미 존재하면 기존 run을 반환해서 여러 gateway 프로세스가 같은 idempotent run을 중복 실행하지 않게 해요.
type RunClaimStore interface {
	ClaimRunWithEvent(ctx context.Context, run Run, event RunEvent) (Run, RunEvent, bool, error)
}
