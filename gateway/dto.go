package gateway

import (
	"encoding/json"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// HealthResponse는 process liveness probe 응답이에요.
type HealthResponse struct {
	OK   bool      `json:"ok"`
	Time time.Time `json:"time"`
}

// ReadyResponse는 gateway가 사용자 요청을 받을 준비가 된 상태를 나타내요.
type ReadyResponse struct {
	Ready bool      `json:"ready"`
	Time  time.Time `json:"time"`
}

// VersionResponse는 gateway 서버와 연결된 runtime 정보를 알려줘요.
type VersionResponse struct {
	Version   string   `json:"version"`
	Commit    string   `json:"commit,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// APIIndexResponse는 adapter가 gateway root에서 주요 discovery link를 찾을 때 쓰는 응답이에요.
type APIIndexResponse struct {
	Version string            `json:"version"`
	Commit  string            `json:"commit,omitempty"`
	Links   map[string]string `json:"links"`
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
	Sessions        []SessionDTO `json:"sessions"`
	Limit           int          `json:"limit,omitempty"`
	Offset          int          `json:"offset,omitempty"`
	NextOffset      int          `json:"next_offset,omitempty"`
	ResultTruncated bool         `json:"result_truncated,omitempty"`
}

// TurnDTO는 웹 패널이 session 대화를 렌더링할 때 쓰는 turn 요약/상세예요.
type TurnDTO struct {
	Seq            int          `json:"seq"`
	ID             string       `json:"id"`
	SessionID      string       `json:"session_id"`
	Prompt         string       `json:"prompt"`
	Model          string       `json:"model,omitempty"`
	Messages       []MessageDTO `json:"messages,omitempty"`
	ResponseID     string       `json:"response_id,omitempty"`
	ResponseStatus string       `json:"response_status,omitempty"`
	ResponseText   string       `json:"response_text,omitempty"`
	Usage          *UsageDTO    `json:"usage,omitempty"`
	StartedAt      time.Time    `json:"started_at,omitempty"`
	EndedAt        time.Time    `json:"ended_at,omitempty"`
	Error          string       `json:"error,omitempty"`
}

type MessageDTO struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
}

type UsageDTO struct {
	InputTokens     int `json:"input_tokens,omitempty"`
	OutputTokens    int `json:"output_tokens,omitempty"`
	TotalTokens     int `json:"total_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type TurnListResponse struct {
	Turns           []TurnDTO `json:"turns"`
	Limit           int       `json:"limit,omitempty"`
	ResultTruncated bool      `json:"result_truncated,omitempty"`
	NextAfterSeq    int       `json:"next_after_seq,omitempty"`
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
	Events          []EventDTO `json:"events"`
	AfterSeq        int        `json:"after_seq,omitempty"`
	Limit           int        `json:"limit,omitempty"`
	ResultTruncated bool       `json:"result_truncated,omitempty"`
	NextAfterSeq    int        `json:"next_after_seq,omitempty"`
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
	Todos           []TodoDTO `json:"todos"`
	TotalTodos      int       `json:"total_todos,omitempty"`
	Limit           int       `json:"limit,omitempty"`
	Offset          int       `json:"offset,omitempty"`
	NextOffset      int       `json:"next_offset,omitempty"`
	ResultTruncated bool      `json:"result_truncated,omitempty"`
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
	// EnabledTools가 비어 있지 않으면 이번 run의 local tool surface를 이 목록으로 제한해요.
	EnabledTools []string `json:"enabled_tools,omitempty"`
	// DisabledTools는 이번 run의 local tool surface에서 제외할 tool 이름 목록이에요.
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// ContextBlocks는 저장 resource 없이 이번 run에만 추가할 provider-neutral prompt context예요.
	ContextBlocks []string `json:"context_blocks,omitempty"`
	// PreviewStream은 /runs/preview에서 provider streaming payload를 확인할 때만 쓰는 힌트예요.
	PreviewStream bool `json:"preview_stream,omitempty"`
	// MaxPreviewBytes는 /runs/preview에서 body/raw/context preview 최대 byte 수를 조절해요.
	MaxPreviewBytes int    `json:"max_preview_bytes,omitempty"`
	RunID           string `json:"-"`
}

// RunDTO는 gateway에서 관리하는 실행 단위예요. gateway RunStarter가 실행을 접수하거나 완료했을 때 생성해요.
type RunDTO struct {
	ID         string   `json:"id"`
	SessionID  string   `json:"session_id"`
	TurnID     string   `json:"turn_id,omitempty"`
	Status     string   `json:"status"`
	Prompt     string   `json:"prompt,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Model      string   `json:"model,omitempty"`
	MCPServers []string `json:"mcp_servers,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Subagents  []string `json:"subagents,omitempty"`
	// EnabledTools/DisabledTools는 실행 당시 local tool surface 선택이에요.
	EnabledTools  []string `json:"enabled_tools,omitempty"`
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// ContextBlocks는 실행 당시 요청에 포함된 임시 prompt context예요.
	ContextBlocks []string          `json:"context_blocks,omitempty"`
	EventsURL     string            `json:"events_url,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	EndedAt       time.Time         `json:"ended_at,omitempty"`
	Error         string            `json:"error,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// RunValidateResponse는 background run queue에 넣기 전 preflight 결과를 외부 adapter에 보여줘요.
type RunValidateResponse struct {
	OK             bool              `json:"ok"`
	Code           string            `json:"code,omitempty"`
	Message        string            `json:"message,omitempty"`
	RequestID      string            `json:"request_id,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	RunID          string            `json:"run_id,omitempty"`
	ExistingRun    *RunDTO           `json:"existing_run,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// RunPreviewResponse는 실제 실행 없이 run 조립 결과를 외부 adapter에 보여줘요.
type RunPreviewResponse struct {
	SessionID         string                     `json:"session_id"`
	ProjectRoot       string                     `json:"project_root,omitempty"`
	Provider          string                     `json:"provider"`
	Model             string                     `json:"model"`
	MCPServers        []ResourceDTO              `json:"mcp_servers,omitempty"`
	Skills            []ResourceDTO              `json:"skills,omitempty"`
	Subagents         []ResourceDTO              `json:"subagents,omitempty"`
	DefaultMCPServers []ResourceDTO              `json:"default_mcp_servers,omitempty"`
	BaseRequestTools  []string                   `json:"base_request_tools,omitempty"`
	LocalTools        []string                   `json:"local_tools,omitempty"`
	ContextBlocks     []string                   `json:"context_blocks,omitempty"`
	ContextTruncated  bool                       `json:"context_truncated,omitempty"`
	ProviderRequest   *ProviderRequestPreviewDTO `json:"provider_request,omitempty"`
}

// ProviderRequestPreviewDTO는 run preview에서 provider API 호출 직전 변환 결과를 보여줘요.
type ProviderRequestPreviewDTO struct {
	Provider      string                   `json:"provider"`
	Operation     string                   `json:"operation,omitempty"`
	Model         string                   `json:"model,omitempty"`
	Stream        bool                     `json:"stream,omitempty"`
	Route         *ProviderRoutePreviewDTO `json:"route,omitempty"`
	BodyJSON      string                   `json:"body_json,omitempty"`
	BodyTruncated bool                     `json:"body_truncated,omitempty"`
	Headers       map[string]string        `json:"headers,omitempty"`
	Metadata      map[string]string        `json:"metadata,omitempty"`
	RawType       string                   `json:"raw_type,omitempty"`
	RawJSON       string                   `json:"raw_json,omitempty"`
	RawTruncated  bool                     `json:"raw_truncated,omitempty"`
}

// ProviderRoutePreviewDTO는 변환된 provider 요청이 실제로 매칭한 route를 보여줘요.
type ProviderRoutePreviewDTO struct {
	Operation     string            `json:"operation"`
	Method        string            `json:"method,omitempty"`
	Path          string            `json:"path"`
	Accept        string            `json:"accept,omitempty"`
	Query         map[string]string `json:"query,omitempty"`
	ResolvedPath  string            `json:"resolved_path,omitempty"`
	ResolvedQuery map[string]string `json:"resolved_query,omitempty"`
}

// RunListResponse는 background run 목록 응답이에요.
type RunListResponse struct {
	Runs            []RunDTO `json:"runs"`
	Limit           int      `json:"limit,omitempty"`
	Offset          int      `json:"offset,omitempty"`
	NextOffset      int      `json:"next_offset,omitempty"`
	ResultTruncated bool     `json:"result_truncated,omitempty"`
}

// RequestCorrelationResponse는 외부 요청 ID로 이어진 run들을 한 번에 보여줘요.
type RequestCorrelationResponse struct {
	RequestID       string   `json:"request_id"`
	Runs            []RunDTO `json:"runs"`
	Limit           int      `json:"limit,omitempty"`
	Offset          int      `json:"offset,omitempty"`
	NextOffset      int      `json:"next_offset,omitempty"`
	ResultTruncated bool     `json:"result_truncated,omitempty"`
}

// RequestCorrelationEventsResponse는 외부 요청 ID로 이어진 run event들을 한 번에 보여줘요.
type RequestCorrelationEventsResponse struct {
	RequestID       string        `json:"request_id"`
	Events          []RunEventDTO `json:"events"`
	Limit           int           `json:"limit,omitempty"`
	ResultTruncated bool          `json:"result_truncated,omitempty"`
}

// RunEventDTO는 run 상태 변경을 SSE/JSON replay로 표현해요.
type RunEventDTO struct {
	Seq     int             `json:"seq"`
	At      time.Time       `json:"at,omitempty"`
	Type    string          `json:"type"`
	Tool    string          `json:"tool,omitempty"`
	Message string          `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Run     RunDTO          `json:"run"`
}

type RunEventListResponse struct {
	Events          []RunEventDTO `json:"events"`
	AfterSeq        int           `json:"after_seq,omitempty"`
	Limit           int           `json:"limit,omitempty"`
	ResultTruncated bool          `json:"result_truncated,omitempty"`
	NextAfterSeq    int           `json:"next_after_seq,omitempty"`
}

// ProviderDTO는 gateway가 알고 있는 provider capability를 설명해요.
type ProviderDTO struct {
	Name         string         `json:"name"`
	Aliases      []string       `json:"aliases,omitempty"`
	Models       []string       `json:"models,omitempty"`
	DefaultModel string         `json:"default_model,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	AuthStatus   string         `json:"auth_status,omitempty"`
	AuthEnv      []string       `json:"auth_env,omitempty"`
	Conversion   *ConversionDTO `json:"conversion,omitempty"`
}

// ConversionDTO는 provider가 표준 요청을 어떤 converter/caller/source로 실행하는지 알려줘요.
type ConversionDTO struct {
	RequestConverter  string     `json:"request_converter,omitempty"`
	ResponseConverter string     `json:"response_converter,omitempty"`
	Call              string     `json:"call,omitempty"`
	Stream            string     `json:"stream,omitempty"`
	Source            string     `json:"source,omitempty"`
	Operations        []string   `json:"operations,omitempty"`
	Routes            []RouteDTO `json:"routes,omitempty"`
}

// RouteDTO는 provider conversion operation이 HTTP source에서 어떤 route를 쓰는지 보여줘요.
type RouteDTO struct {
	Operation string            `json:"operation"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path"`
	Accept    string            `json:"accept,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
}

type ProviderListResponse struct {
	Providers []ProviderDTO `json:"providers"`
}

// ProviderTestRequest는 session 없이 provider 변환/인증/live smoke를 점검할 때 써요.
type ProviderTestRequest struct {
	Model           string            `json:"model,omitempty"`
	Prompt          string            `json:"prompt,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	Live            bool              `json:"live,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	MaxPreviewBytes int               `json:"max_preview_bytes,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	MaxResultBytes  int               `json:"max_result_bytes,omitempty"`
	TimeoutMS       int               `json:"timeout_ms,omitempty"`
}

// ProviderTestResultDTO는 provider live smoke 결과를 adapter 친화적으로 요약해요.
type ProviderTestResultDTO struct {
	ID            string    `json:"id,omitempty"`
	Status        string    `json:"status,omitempty"`
	Text          string    `json:"text,omitempty"`
	TextBytes     int       `json:"text_bytes,omitempty"`
	TextTruncated bool      `json:"text_truncated,omitempty"`
	Usage         *UsageDTO `json:"usage,omitempty"`
}

// ProviderTestResponse는 provider 단독 preflight 결과예요.
type ProviderTestResponse struct {
	OK              bool                       `json:"ok"`
	Provider        string                     `json:"provider"`
	Model           string                     `json:"model,omitempty"`
	AuthStatus      string                     `json:"auth_status,omitempty"`
	Live            bool                       `json:"live,omitempty"`
	Stream          bool                       `json:"stream,omitempty"`
	Code            string                     `json:"code,omitempty"`
	Message         string                     `json:"message,omitempty"`
	ProviderRequest *ProviderRequestPreviewDTO `json:"provider_request,omitempty"`
	Result          *ProviderTestResultDTO     `json:"result,omitempty"`
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
	Version              string                     `json:"version"`
	Commit               string                     `json:"commit,omitempty"`
	Features             []FeatureDTO               `json:"features"`
	Providers            []ProviderDTO              `json:"providers"`
	ProviderCapabilities []ProviderCapabilityKeyDTO `json:"provider_capabilities,omitempty"`
	ProviderPipeline     []ProviderPipelineStageDTO `json:"provider_pipeline,omitempty"`
	DefaultMCPServers    []ResourceDTO              `json:"default_mcp_servers,omitempty"`
	Limits               LimitDTO                   `json:"limits"`
}

// ProviderCapabilityKeyDTO는 provider capability map에 나올 수 있는 key와 의미를 설명해요.
// 각 provider의 capability map은 true 값만 짧게 노출하므로, adapter는 이 catalog를 기준으로 빠진 key를 false처럼 해석하면 돼요.
type ProviderCapabilityKeyDTO struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProviderPipelineStageDTO는 표준 요청이 source 호출까지 지나가는 변환 단계를 설명해요.
// 외부 adapter는 이 순서를 기준으로 preview, live test, 실제 run UI를 같은 mental model로 그리면 돼요.
type ProviderPipelineStageDTO struct {
	Name        string `json:"name"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	Description string `json:"description,omitempty"`
}

// DiagnosticsResponse는 배포/adapter 연결 상태를 한 번에 점검하는 운영 응답이에요.
type DiagnosticsResponse struct {
	OK                   bool                 `json:"ok"`
	Version              string               `json:"version"`
	Commit               string               `json:"commit,omitempty"`
	Time                 time.Time            `json:"time"`
	Checks               []DiagnosticCheckDTO `json:"checks"`
	Providers            int                  `json:"providers"`
	Features             int                  `json:"features"`
	DefaultMCPServers    int                  `json:"default_mcp_servers"`
	MaxRequestBytes      int64                `json:"max_request_bytes"`
	MaxConcurrentRuns    int                  `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSeconds    int                  `json:"run_timeout_seconds,omitempty"`
	MissingRuntimeWiring []string             `json:"missing_runtime_wiring,omitempty"`
	FailingChecks        []string             `json:"failing_checks,omitempty"`
	RunRuntime           *RunRuntimeStatsDTO  `json:"run_runtime,omitempty"`
}

type DiagnosticCheckDTO struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// RunRuntimeStatsDTO는 dashboard가 현재 process-local run queue를 그릴 때 쓰는 운영 상태예요.
type RunRuntimeStatsDTO struct {
	TrackedRuns       int `json:"tracked_runs"`
	ActiveRuns        int `json:"active_runs"`
	QueuedRuns        int `json:"queued_runs"`
	RunningRuns       int `json:"running_runs"`
	CancellingRuns    int `json:"cancelling_runs"`
	TerminalRuns      int `json:"terminal_runs"`
	MaxConcurrentRuns int `json:"max_concurrent_runs,omitempty"`
	OccupiedRunSlots  int `json:"occupied_run_slots,omitempty"`
	AvailableRunSlots int `json:"available_run_slots,omitempty"`
	RunTimeoutSeconds int `json:"run_timeout_seconds,omitempty"`
}

// LimitDTO는 외부 adapter가 payload와 polling 전략을 맞출 때 보는 gateway 제한값이에요.
type LimitDTO struct {
	MaxRequestBytes         int64 `json:"max_request_bytes"`
	MaxConcurrentRuns       int   `json:"max_concurrent_runs,omitempty"`
	RunTimeoutSeconds       int   `json:"run_timeout_seconds,omitempty"`
	MaxMCPHTTPResponseBytes int   `json:"max_mcp_http_response_bytes,omitempty"`
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

func toTurnDTO(sessionID string, seq int, turn session.Turn) TurnDTO {
	out := TurnDTO{
		Seq:       seq,
		ID:        turn.ID,
		SessionID: sessionID,
		Prompt:    turn.Prompt,
		Model:     turn.Request.Model,
		Messages:  toMessageDTOs(turn.Request.Messages),
		StartedAt: turn.StartedAt,
		EndedAt:   turn.EndedAt,
		Error:     turn.Error,
	}
	if turn.Response != nil {
		out.ResponseID = turn.Response.ID
		out.ResponseStatus = turn.Response.Status
		out.ResponseText = turn.Response.Text
		out.Usage = toUsageDTO(turn.Response.Usage)
		if out.Model == "" {
			out.Model = turn.Response.Model
		}
	}
	return out
}

func toMessageDTOs(messages []llm.Message) []MessageDTO {
	if len(messages) == 0 {
		return nil
	}
	out := make([]MessageDTO, 0, len(messages))
	for _, message := range messages {
		out = append(out, MessageDTO{Role: string(message.Role), Content: message.Content})
	}
	return out
}

func toUsageDTO(usage llm.Usage) *UsageDTO {
	if usage == (llm.Usage{}) {
		return nil
	}
	return &UsageDTO{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, ReasoningTokens: usage.ReasoningTokens}
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
