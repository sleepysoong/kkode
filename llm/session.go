package llm

import "context"

// SessionProvider creates long-lived model sessions. Agent runtimes such as
// Copilot SDK and Codex CLI/App Server can implement this in addition to the
// one-shot Provider interface.
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
	ApprovalPolicy   ApprovalPolicy
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
