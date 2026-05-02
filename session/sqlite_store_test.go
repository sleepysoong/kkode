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

func TestSQLiteTimelineStoreListsTurnsAndEventsWithoutFullSession(t *testing.T) {
	ctx := context.Background()
	store := openSQLiteForTest(t)
	sess := NewSession("/repo", "openai", "gpt-5-mini", "agent", AgentModeBuild)
	for _, prompt := range []string{"첫 번째", "두 번째", "세 번째"} {
		turn := NewTurn(prompt, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText(prompt)}})
		turn.Response = &llm.Response{ID: prompt, Text: prompt}
		turn.EndedAt = turn.StartedAt
		sess.AppendTurn(turn)
		sess.AppendEvent(Event{ID: NewID("ev"), SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed"})
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	turns, err := store.ListTurns(ctx, TurnQuery{SessionID: sess.ID, AfterSeq: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Seq != 2 || turns[0].Turn.Prompt != "두 번째" || turns[0].Turn.Response.Text != "두 번째" {
		t.Fatalf("timeline turns가 이상해요: %+v", turns)
	}

	loadedTurn, err := store.LoadTurn(ctx, sess.ID, sess.Turns[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedTurn.Seq != 3 || loadedTurn.Turn.Prompt != "세 번째" {
		t.Fatalf("timeline turn load가 이상해요: %+v", loadedTurn)
	}

	events, err := store.ListEvents(ctx, EventQuery{SessionID: sess.ID, AfterSeq: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 3 || events[0].Event.TurnID != sess.Turns[2].ID {
		t.Fatalf("timeline events가 이상해요: %+v", events)
	}

	if _, err := store.ListTurns(ctx, TurnQuery{SessionID: "missing", Limit: 1}); err == nil {
		t.Fatal("없는 session timeline은 오류를 내야 해요")
	}
	if _, err := store.LoadTurn(ctx, sess.ID, "missing_turn"); err == nil {
		t.Fatal("없는 turn은 오류를 내야 해요")
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

func TestRunStorePersistsBackgroundRuns(t *testing.T) {
	ctx := context.Background()
	store := openSQLiteForTest(t)
	sess := NewSession("/repo", "openai", "gpt", "agent", AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveRun(ctx, Run{ID: "run_1", SessionID: sess.ID, Status: "queued", Prompt: "go", EventsURL: "/api/v1/sessions/" + sess.ID + "/events", Metadata: map[string]string{"source": "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "run_1" || saved.Status != "queued" {
		t.Fatalf("saved run이 이상해요: %+v", saved)
	}
	saved.Status = "completed"
	saved.TurnID = "turn_1"
	if _, err := store.SaveRun(ctx, saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRun(ctx, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "completed" || loaded.TurnID != "turn_1" || loaded.Metadata["source"] != "test" {
		t.Fatalf("loaded run이 이상해요: %+v", loaded)
	}
	listed, err := store.ListRuns(ctx, RunQuery{SessionID: sess.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "run_1" {
		t.Fatalf("run list가 이상해요: %+v", listed)
	}
}

func TestRunEventStorePersistsReplay(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession("/repo", "openai", "gpt", "agent", AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run, err := store.SaveRun(ctx, Run{ID: "run_1", SessionID: sess.ID, Status: "queued", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AppendRunEvent(ctx, RunEvent{RunID: run.ID, Type: "run.queued", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	if _, err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendRunEvent(ctx, RunEvent{RunID: run.ID, Type: "run.completed", Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("run event seq가 이상해요: %d %d", first.Seq, second.Seq)
	}
	replay, err := store.ListRunEvents(ctx, RunEventQuery{RunID: run.ID, AfterSeq: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Type != "run.completed" || replay[0].Run.Status != "completed" {
		t.Fatalf("run event replay가 이상해요: %+v", replay)
	}
}

func TestCheckpointStoreListsAndLoads(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession("/repo", "openai", "gpt", "agent", AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	cp := Checkpoint{ID: "cp_1", SessionID: sess.ID, TurnID: "turn_1", Payload: []byte(`{"summary":"ok"}`)}
	if err := store.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCheckpoint(ctx, sess.ID, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != cp.ID || string(loaded.Payload) != `{"summary":"ok"}` {
		t.Fatalf("checkpoint load가 이상해요: %+v", loaded)
	}
	items, err := store.ListCheckpoints(ctx, CheckpointQuery{SessionID: sess.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != cp.ID {
		t.Fatalf("checkpoint list가 이상해요: %+v", items)
	}
}

func TestTodoToolsUseDedicatedSaveTodosWhenAvailable(t *testing.T) {
	ctx := context.Background()
	store := &trackingTodoStore{sess: NewSession("/repo", "openai", "gpt", "build", AgentModeBuild)}
	_, handlers := TodoTools(store, store.sess.ID)
	args, _ := json.Marshal(map[string]any{"items": []map[string]any{{"content": "원자 저장", "status": "pending"}}})
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "todo_write", CallID: "call_1", Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if store.saveTodosCalls != 1 || store.saveSessionCalls != 0 {
		t.Fatalf("전용 SaveTodos를 써야 해요: saveTodos=%d saveSession=%d", store.saveTodosCalls, store.saveSessionCalls)
	}
	if len(store.sess.Todos) != 1 || store.sess.Todos[0].ID == "" || store.sess.Todos[0].UpdatedAt.IsZero() {
		t.Fatalf("todo normalize/save가 이상해요: %+v", store.sess.Todos)
	}
}

type trackingTodoStore struct {
	sess             *Session
	saveTodosCalls   int
	saveSessionCalls int
}

func (s *trackingTodoStore) LoadSession(ctx context.Context, id string) (*Session, error) {
	clone := *s.sess
	clone.Todos = append([]Todo(nil), s.sess.Todos...)
	return &clone, nil
}

func (s *trackingTodoStore) SaveSession(ctx context.Context, sess *Session) error {
	s.saveSessionCalls++
	s.sess = sess
	return nil
}

func (s *trackingTodoStore) SaveTodos(ctx context.Context, sessionID string, todos []Todo) error {
	s.saveTodosCalls++
	s.sess.Todos = append([]Todo(nil), todos...)
	s.sess.Touch()
	return nil
}

func TestAppendEventPersistsOrderedSessionEvents(t *testing.T) {
	ctx := context.Background()
	store := openSQLiteForTest(t)
	sess := NewSession("/repo", "openai", "gpt", "agent", AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"turn.started", "tool.completed", "turn.completed"} {
		if err := store.AppendEvent(ctx, Event{SessionID: sess.ID, TurnID: "turn_1", Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 3 || loaded.Events[0].Type != "turn.started" || loaded.Events[2].Type != "turn.completed" {
		t.Fatalf("event ordinal replay가 이상해요: %+v", loaded.Events)
	}
	if !loaded.UpdatedAt.After(sess.UpdatedAt) && !loaded.UpdatedAt.Equal(sess.UpdatedAt) {
		t.Fatalf("session updated_at이 보존되지 않았어요: before=%s after=%s", sess.UpdatedAt, loaded.UpdatedAt)
	}
}
