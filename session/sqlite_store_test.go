package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestSQLiteStoreSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess := NewSession("/repo", "openai", "gpt-5-mini", "build", AgentModeBuild)
	turn := NewTurn("안녕", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}})
	turn.Response = &llm.Response{ID: "resp_1", Text: "반가워요", Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: "반가워요"}}}
	turn.EndedAt = turn.StartedAt
	sess.AppendTurn(turn)
	sess.AppendEvent(Event{ID: "ev_1", SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed"})
	sess.Todos = []Todo{{ID: "todo_1", Content: "테스트", Status: TodoCompleted}}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != sess.ID || len(loaded.Turns) != 1 || loaded.Turns[0].Response.Text != "반가워요" {
		t.Fatalf("loaded=%#v", loaded)
	}
	if loaded.LastResponseID != "resp_1" || len(loaded.LastInputItems) != 1 {
		t.Fatalf("last state missing: %#v", loaded)
	}
	if len(loaded.Events) != 1 || len(loaded.Todos) != 1 {
		t.Fatalf("events/todos missing: %#v", loaded)
	}

	summaries, err := store.ListSessions(ctx, SessionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].TurnCount != 1 {
		t.Fatalf("summaries=%#v", summaries)
	}
}

func TestForkSession(t *testing.T) {
	sess := NewSession("/repo", "openai", "gpt", "build", AgentModeBuild)
	for _, prompt := range []string{"첫 번째", "두 번째"} {
		turn := NewTurn(prompt, llm.Request{Model: "gpt"})
		turn.Response = &llm.Response{ID: prompt, Text: prompt}
		turn.EndedAt = turn.StartedAt
		sess.AppendTurn(turn)
		sess.AppendEvent(Event{ID: NewID("ev"), SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed"})
	}
	forked, err := ForkSession(sess, sess.Turns[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if forked.ID == sess.ID || forked.Metadata["forked_from"] != sess.ID {
		t.Fatalf("fork metadata broken: %#v", forked)
	}
	if len(forked.Turns) != 1 || forked.Turns[0].Prompt != "첫 번째" {
		t.Fatalf("turns=%#v", forked.Turns)
	}
	if len(forked.Events) != 1 || forked.Events[0].SessionID != forked.ID {
		t.Fatalf("events=%#v", forked.Events)
	}
}

func TestTodoToolsPersist(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession("/repo", "openai", "gpt", "build", AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	_, handlers := TodoTools(store, sess.ID)
	args, _ := json.Marshal(map[string]any{"items": []map[string]any{{"id": "todo_1", "content": "구현", "status": "in_progress", "priority": "high"}}})
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "todo_write", CallID: "call_1", Arguments: args}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Todos) != 1 || loaded.Todos[0].Content != "구현" {
		t.Fatalf("todos=%#v", loaded.Todos)
	}
}

func TestResourceStorePersistsManifests(t *testing.T) {
	ctx := context.Background()
	store := openSQLiteForTest(t)
	enabled := true
	saved, err := store.SaveResource(ctx, Resource{Kind: ResourceMCPServer, Name: "fs", Description: "filesystem mcp", Enabled: enabled, Config: []byte(`{"command":"mcp-fs"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Kind != ResourceMCPServer {
		t.Fatalf("saved resource가 이상해요: %+v", saved)
	}
	loaded, err := store.LoadResource(ctx, ResourceMCPServer, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "fs" || string(loaded.Config) != `{"command":"mcp-fs"}` {
		t.Fatalf("loaded resource가 이상해요: %+v", loaded)
	}
	items, err := store.ListResources(ctx, ResourceQuery{Kind: ResourceMCPServer, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != saved.ID {
		t.Fatalf("resource list가 이상해요: %+v", items)
	}
	if err := store.DeleteResource(ctx, ResourceMCPServer, saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadResource(ctx, ResourceMCPServer, saved.ID); err == nil {
		t.Fatal("삭제한 resource는 다시 읽히면 안 돼요")
	}
}

func openSQLiteForTest(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
