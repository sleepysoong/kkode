package session

import (
	"context"
	"encoding/json"
	"time"
)

// Artifact는 웹/Discord adapter가 큰 tool output, diff, generated file preview를
// event payload와 분리해서 조회할 수 있게 저장하는 session 산출물이에요.
type Artifact struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	RunID     string            `json:"run_id,omitempty"`
	TurnID    string            `json:"turn_id,omitempty"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name,omitempty"`
	MimeType  string            `json:"mime_type,omitempty"`
	Content   json.RawMessage   `json:"content,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type ArtifactQuery struct {
	SessionID string
	RunID     string
	TurnID    string
	Kind      string
	Limit     int
	Offset    int
}

type ArtifactStore interface {
	SaveArtifact(ctx context.Context, artifact Artifact) (Artifact, error)
	LoadArtifact(ctx context.Context, id string) (Artifact, error)
	ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error)
	DeleteArtifact(ctx context.Context, id string) error
}
