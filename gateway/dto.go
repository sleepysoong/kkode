package gateway

import (
	"encoding/json"
	"time"

	"github.com/sleepysoong/kkode/session"
)

// VersionResponse는 gateway 서버와 연결된 runtime 정보를 알려줘요.
type VersionResponse struct {
	Version   string   `json:"version"`
	Commit    string   `json:"commit,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// SessionCreateRequest는 웹 패널이나 Discord adapter가 새 agent session을 만들 때 쓰는 요청이에요.
type SessionCreateRequest struct {
	ProjectRoot string            `json:"project_root"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Agent       string            `json:"agent,omitempty"`
	Mode        string            `json:"mode,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// SessionDTO는 외부 API에 노출하는 session 요약이에요.
type SessionDTO struct {
	ID             string            `json:"id"`
	ProjectRoot    string            `json:"project_root"`
	ProviderName   string            `json:"provider_name"`
	Model          string            `json:"model"`
	AgentName      string            `json:"agent_name"`
	Mode           string            `json:"mode"`
	Summary        string            `json:"summary,omitempty"`
	TurnCount      int               `json:"turn_count"`
	EventCount     int               `json:"event_count,omitempty"`
	TodoCount      int               `json:"todo_count,omitempty"`
	LastResponseID string            `json:"last_response_id,omitempty"`
	CreatedAt      time.Time         `json:"created_at,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type SessionListResponse struct {
	Sessions []SessionDTO `json:"sessions"`
}

// EventDTO는 session event를 API cursor와 함께 표현해요.
type EventDTO struct {
	Seq       int             `json:"seq"`
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	At        time.Time       `json:"at"`
	Type      string          `json:"type"`
	Tool      string          `json:"tool,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type EventListResponse struct {
	Events []EventDTO `json:"events"`
}

// TodoDTO는 웹 패널/Discord status message에서 그대로 보여줄 수 있는 작업 항목이에요.
type TodoDTO struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Priority  string    `json:"priority,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TodoListResponse struct {
	Todos []TodoDTO `json:"todos"`
}

// RunStartRequest는 gateway RunStarter가 실제 agent 실행에 넘기는 요청이에요.
type RunStartRequest struct {
	SessionID  string            `json:"session_id"`
	Prompt     string            `json:"prompt"`
	Provider   string            `json:"provider,omitempty"`
	Model      string            `json:"model,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	MCPServers []string          `json:"mcp_servers,omitempty"`
	Skills     []string          `json:"skills,omitempty"`
	Subagents  []string          `json:"subagents,omitempty"`
	RunID      string            `json:"-"`
}

// RunDTO는 gateway에서 관리하는 실행 단위예요. gateway RunStarter가 실행을 접수하거나 완료했을 때 생성해요.
type RunDTO struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	TurnID    string            `json:"turn_id,omitempty"`
	Status    string            `json:"status"`
	Prompt    string            `json:"prompt,omitempty"`
	EventsURL string            `json:"events_url,omitempty"`
	StartedAt time.Time         `json:"started_at,omitempty"`
	EndedAt   time.Time         `json:"ended_at,omitempty"`
	Error     string            `json:"error,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// RunListResponse는 background run 목록 응답이에요.
type RunListResponse struct {
	Runs []RunDTO `json:"runs"`
}

// RunEventDTO는 run 상태 변경을 SSE/JSON replay로 표현해요.
type RunEventDTO struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at,omitempty"`
	Type string    `json:"type"`
	Run  RunDTO    `json:"run"`
}

type RunEventListResponse struct {
	Events []RunEventDTO `json:"events"`
}

// ProviderDTO는 gateway가 알고 있는 provider capability를 설명해요.
type ProviderDTO struct {
	Name         string         `json:"name"`
	Models       []string       `json:"models,omitempty"`
	DefaultModel string         `json:"default_model,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	AuthStatus   string         `json:"auth_status,omitempty"`
}

type ProviderListResponse struct {
	Providers []ProviderDTO `json:"providers"`
}

// ModelDTO는 외부 adapter가 모델 선택 UI를 만들 때 쓰는 정규화된 모델 항목이에요.
type ModelDTO struct {
	ID           string         `json:"id"`
	Provider     string         `json:"provider"`
	DisplayName  string         `json:"display_name,omitempty"`
	Default      bool           `json:"default,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	AuthStatus   string         `json:"auth_status,omitempty"`
}

type ModelListResponse struct {
	Models []ModelDTO `json:"models"`
}

// FeatureDTO는 외부 adapter가 kkode gateway 기능 상태와 endpoint를 발견할 때 쓰는 항목이에요.
type FeatureDTO struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Description string   `json:"description,omitempty"`
	Endpoints   []string `json:"endpoints,omitempty"`
}

// CapabilityResponse는 gateway feature discovery 응답이에요.
type CapabilityResponse struct {
	Version   string        `json:"version"`
	Commit    string        `json:"commit,omitempty"`
	Features  []FeatureDTO  `json:"features"`
	Providers []ProviderDTO `json:"providers"`
}

func toSessionDTO(sess *session.Session) SessionDTO {
	if sess == nil {
		return SessionDTO{}
	}
	return SessionDTO{
		ID:             sess.ID,
		ProjectRoot:    sess.ProjectRoot,
		ProviderName:   sess.ProviderName,
		Model:          sess.Model,
		AgentName:      sess.AgentName,
		Mode:           string(sess.Mode),
		Summary:        sess.Summary,
		TurnCount:      len(sess.Turns),
		EventCount:     len(sess.Events),
		TodoCount:      len(sess.Todos),
		LastResponseID: sess.LastResponseID,
		CreatedAt:      sess.CreatedAt,
		UpdatedAt:      sess.UpdatedAt,
		Metadata:       cloneMap(sess.Metadata),
	}
}

func toSessionSummaryDTO(summary session.SessionSummary) SessionDTO {
	return SessionDTO{
		ID:           summary.ID,
		ProjectRoot:  summary.ProjectRoot,
		ProviderName: summary.ProviderName,
		Model:        summary.Model,
		AgentName:    summary.AgentName,
		Mode:         string(summary.Mode),
		Summary:      summary.Summary,
		TurnCount:    summary.TurnCount,
		UpdatedAt:    summary.UpdatedAt,
	}
}

func toEventDTO(seq int, ev session.Event) EventDTO {
	return EventDTO{
		Seq:       seq,
		ID:        ev.ID,
		SessionID: ev.SessionID,
		TurnID:    ev.TurnID,
		At:        ev.At,
		Type:      ev.Type,
		Tool:      ev.Tool,
		Payload:   ev.Payload,
		Error:     ev.Error,
	}
}

func toTodoDTO(todo session.Todo) TodoDTO {
	return TodoDTO{
		ID:        todo.ID,
		Content:   todo.Content,
		Status:    string(todo.Status),
		Priority:  todo.Priority,
		UpdatedAt: todo.UpdatedAt,
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
