package llm

import (
	"context"
	"encoding/json"
)

// Provider는 model backend를 위한 가장 작은 공통 인터페이스예요.
// 구현체는 가능한 경우 provider별 output item을 Item.Raw 또는 ProviderRaw에 보존해야해요.
// 그래야 상위 loop가 reasoning/tool 상태를 잃지 않아요.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Generate(ctx context.Context, req Request) (*Response, error)
}

type Capabilities struct {
	Tools              bool
	CustomTools        bool
	Reasoning          bool
	ReasoningSummaries bool
	StructuredOutput   bool
	Streaming          bool
	ToolChoice         bool
	ParallelToolCalls  bool
	PromptRefs         bool
	PreviousResponseID bool
	MCP                bool
	Skills             bool
	CustomAgents       bool
}

type Auth struct {
	Type    AuthType
	Token   string
	Headers map[string]string
}

type AuthType string

const (
	AuthNone   AuthType = "none"
	AuthBearer AuthType = "bearer"
	AuthAPIKey AuthType = "api_key"
	AuthOAuth  AuthType = "oauth"
	AuthLocal  AuthType = "local"
)

type Request struct {
	Model              string
	Instructions       string
	Messages           []Message
	InputItems         []Item
	Prompt             *PromptRef
	Tools              []Tool
	ToolChoice         ToolChoice
	Reasoning          *ReasoningConfig
	TextFormat         *TextFormat
	MaxOutputTokens    int
	MaxToolCalls       int
	Temperature        *float64
	TopP               *float64
	Store              *bool
	PreviousResponseID string
	Include            []string
	Metadata           map[string]string
	ParallelToolCalls  *bool
	SafetyIdentifier   string
	PromptCacheKey     string
}

type PromptRef struct {
	ID        string
	Version   string
	Variables map[string]any
}

type Message struct {
	Role    Role
	Content string
	Parts   []ContentPart
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentPart struct {
	Type     string
	Text     string
	ImageURL string
	FileID   string
	Raw      json.RawMessage
}

type ToolKind string

const (
	ToolFunction ToolKind = "function"
	ToolCustom   ToolKind = "custom"
	ToolBuiltin  ToolKind = "builtin"
)

type Tool struct {
	Kind        ToolKind
	Name        string
	Description string
	Parameters  map[string]any
	Strict      *bool
	Grammar     *Grammar
	// ProviderOptions는 core model을 오염시키지 않고 backend별 설정을 전달해요.
	ProviderOptions map[string]any
}

type Grammar struct {
	Syntax     string
	Definition string
}

type ToolChoiceMode string

const (
	ToolChoiceZero     ToolChoiceMode = ""
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceFunction ToolChoiceMode = "function"
	ToolChoiceAllowed  ToolChoiceMode = "allowed_tools"
)

type ToolChoice struct {
	Mode         ToolChoiceMode
	Name         string
	AllowedTools []string
}

type ReasoningConfig struct {
	Effort  string
	Summary string
}

type TextFormat struct {
	Type        string
	Name        string
	Description string
	Schema      map[string]any
	Strict      bool
}

type Response struct {
	ID                 string
	Provider           string
	Model              string
	Status             string
	Text               string
	Output             []Item
	ToolCalls          []ToolCall
	Reasoning          []ReasoningItem
	Usage              Usage
	PreviousResponseID string
	Raw                json.RawMessage
}

type ItemType string

const (
	ItemMessage          ItemType = "message"
	ItemFunctionCall     ItemType = "function_call"
	ItemCustomToolCall   ItemType = "custom_tool_call"
	ItemFunctionOutput   ItemType = "function_call_output"
	ItemCustomToolOutput ItemType = "custom_tool_call_output"
	ItemReasoning        ItemType = "reasoning"
	ItemUnknown          ItemType = "unknown"
)

type Item struct {
	Type        ItemType
	Role        Role
	Content     string
	ToolCall    *ToolCall
	ToolResult  *ToolResult
	Reasoning   *ReasoningItem
	ProviderRaw json.RawMessage
}

type ToolCall struct {
	ID        string
	CallID    string
	Name      string
	Arguments json.RawMessage
	Custom    bool
}

type ToolResult struct {
	CallID  string
	Name    string
	Output  string
	Error   string
	Custom  bool
	RawJSON json.RawMessage
}

type ReasoningItem struct {
	ID               string
	Summary          []string
	Text             []string
	EncryptedContent string
	Raw              json.RawMessage
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ReasoningTokens int
}

func Bool(v bool) *bool          { return &v }
func Float64(v float64) *float64 { return &v }
