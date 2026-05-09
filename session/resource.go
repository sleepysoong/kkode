package session

import (
	"context"
	"encoding/json"
	"time"
)

type ResourceKind string

const (
	ResourceMCPServer ResourceKind = "mcp_server"
	ResourceSkill     ResourceKind = "skill"
	ResourceSubagent  ResourceKind = "subagent"
)

// Resource는 gateway가 MCP server, skill, subagent 같은 외부 실행 자산을 영속화하는 공통 레코드예요.
type Resource struct {
	ID          string          `json:"id"`
	Kind        ResourceKind    `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Enabled     bool            `json:"enabled"`
	Config      json.RawMessage `json:"config,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ResourceQuery struct {
	Kind    ResourceKind
	Name    string
	Enabled *bool
	Limit   int
	Offset  int
}

type ResourceStore interface {
	SaveResource(ctx context.Context, resource Resource) (Resource, error)
	LoadResource(ctx context.Context, kind ResourceKind, id string) (Resource, error)
	ListResources(ctx context.Context, q ResourceQuery) ([]Resource, error)
	DeleteResource(ctx context.Context, kind ResourceKind, id string) error
}

type ResourceCounter interface {
	CountResources(ctx context.Context, q ResourceQuery) (int, error)
}
