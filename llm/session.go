package llm

import "context"

// SessionProvider는 오래 유지되는 model session을 만들어요.
// Copilot SDK나 Codex CLI/App Server 같은 agent runtime은 단발 Provider와 함께 이 인터페이스도 구현할 수 있어요.
type SessionProvider interface {
	Provider
	NewSession(ctx context.Context, req SessionRequest) (Session, error)
}

type SessionRequest struct {
	Model            string
	Instructions     string
	WorkingDirectory string
	Tools            []Tool
	MCPServers       map[string]MCPServer
	Skills           []string
	CustomAgents     []Agent
	Reasoning        *ReasoningConfig
	ProviderOptions  map[string]any
}

type Session interface {
	ID() string
	Send(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) (EventStream, error)
	Close() error
}

type Agent struct {
	Name        string
	DisplayName string
	Description string
	Prompt      string
	Tools       []string
	MCPServers  map[string]MCPServer
	Infer       *bool
	Skills      []string
}

type MCPServerKind string

const (
	MCPStdio MCPServerKind = "stdio"
	MCPHTTP  MCPServerKind = "http"
)

type MCPServer struct {
	Kind    MCPServerKind
	Name    string
	Tools   []string
	Timeout int
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	URL     string
	Headers map[string]string
}
