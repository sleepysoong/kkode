package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/prompts"
	tooldefs "github.com/sleepysoong/kkode/tools"
)

type todoSaver interface {
	LoadSession(ctx context.Context, id string) (*Session, error)
	SaveSession(ctx context.Context, s *Session) error
}

func TodoTools(store todoSaver, sessionID string) ([]llm.Tool, llm.ToolRegistry) {
	strict := true
	defs := []llm.Tool{
		{Kind: llm.ToolFunction, Name: "todo_write", Description: "현재 session의 todo 목록 전체를 저장해요", Strict: &strict, Parameters: tooldefs.ObjectSchema(map[string]any{"items": tooldefs.ArraySchema(tooldefs.ObjectSchema(map[string]any{"id": tooldefs.StringSchema(), "content": tooldefs.StringSchema(), "status": tooldefs.StringSchema(), "priority": tooldefs.StringSchema()}))})},
		{Kind: llm.ToolFunction, Name: "todo_update", Description: "현재 session의 todo 하나를 갱신해요", Strict: &strict, Parameters: tooldefs.ObjectSchema(map[string]any{"id": tooldefs.StringSchema(), "content": tooldefs.StringSchema(), "status": tooldefs.StringSchema(), "priority": tooldefs.StringSchema()})},
		{Kind: llm.ToolFunction, Name: "todo_list", Description: "현재 session의 todo 목록을 읽어요", Strict: &strict, Parameters: tooldefs.ObjectSchema(map[string]any{})},
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

func TodoInstructions() string {
	text, err := prompts.Text(prompts.TodoInstructions)
	if err != nil {
		return "복잡한 작업은 todo_write/todo_update/todo_list 도구로 진행 상황을 관리해야해요."
	}
	return strings.TrimSpace(text)
}
