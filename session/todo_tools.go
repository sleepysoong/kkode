package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

type todoSaver interface {
	LoadSession(ctx context.Context, id string) (*Session, error)
	SaveSession(ctx context.Context, s *Session) error
}

func TodoTools(store todoSaver, sessionID string) ([]llm.Tool, llm.ToolRegistry) {
	strict := true
	defs := []llm.Tool{
		{Kind: llm.ToolFunction, Name: "todo_write", Description: "현재 session의 todo 목록 전체를 저장해요", Strict: &strict, Parameters: objectSchema(map[string]any{"items": arraySchema(objectSchema(map[string]any{"id": stringSchema(), "content": stringSchema(), "status": stringSchema(), "priority": stringSchema()}))})},
		{Kind: llm.ToolFunction, Name: "todo_update", Description: "현재 session의 todo 하나를 갱신해요", Strict: &strict, Parameters: objectSchema(map[string]any{"id": stringSchema(), "content": stringSchema(), "status": stringSchema(), "priority": stringSchema()})},
		{Kind: llm.ToolFunction, Name: "todo_list", Description: "현재 session의 todo 목록을 읽어요", Strict: &strict, Parameters: objectSchema(map[string]any{})},
	}
	handlers := llm.ToolRegistry{
		"todo_write": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Items []Todo `json:"items"`
		}) (string, error) {
			s, err := store.LoadSession(ctx, sessionID)
			if err != nil {
				return "", err
			}
			for i := range in.Items {
				normalizeTodo(&in.Items[i])
			}
			s.Todos = in.Items
			s.Touch()
			if err := store.SaveSession(ctx, s); err != nil {
				return "", err
			}
			return todoText(in.Items), nil
		}),
		"todo_update": llm.JSONToolHandler(func(ctx context.Context, in Todo) (string, error) {
			s, err := store.LoadSession(ctx, sessionID)
			if err != nil {
				return "", err
			}
			if in.ID == "" {
				in.ID = NewID("todo")
			}
			if in.UpdatedAt.IsZero() {
				in.UpdatedAt = time.Now().UTC()
			}
			if in.Status == "" {
				in.Status = TodoPending
			}
			var updated bool
			for i := range s.Todos {
				if s.Todos[i].ID == in.ID {
					s.Todos[i] = in
					updated = true
					break
				}
			}
			if !updated {
				s.Todos = append(s.Todos, in)
			}
			s.Touch()
			if err := store.SaveSession(ctx, s); err != nil {
				return "", err
			}
			return todoText(s.Todos), nil
		}),
		"todo_list": func(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
			s, err := store.LoadSession(ctx, sessionID)
			if err != nil {
				return llm.ToolResult{}, err
			}
			out := todoText(s.Todos)
			return llm.ToolResult{CallID: call.CallID, Name: call.Name, Output: out}, nil
		},
	}
	return defs, handlers
}

func todoText(items []Todo) string {
	if len(items) == 0 {
		return "todo가 비어 있어요"
	}
	b, _ := json.MarshalIndent(items, "", "  ")
	return string(b)
}

func objectSchema(properties map[string]any) map[string]any {
	required := make([]any, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func TodoInstructions() string {
	return strings.TrimSpace("복잡한 작업은 todo_write/todo_update/todo_list 도구로 진행 상황을 관리해야해요.")
}
