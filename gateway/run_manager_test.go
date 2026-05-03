package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/session"
)

func TestAsyncRunManagerStartsAndCompletesRun(t *testing.T) {
	started := make(chan RunStartRequest, 1)
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		started <- req
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", TurnID: "turn_1"}, nil
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "go", Metadata: map[string]string{"source": "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.Status != "queued" || run.EventsURL != "/api/v1/sessions/sess_1/events" {
		t.Fatalf("접수 run이 이상해요: %+v", run)
	}
	select {
	case req := <-started:
		if req.RunID != run.ID {
			t.Fatalf("run id를 starter에 넘겨야 해요: %q != %q", req.RunID, run.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("starter가 실행되지 않았어요")
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	listed, err := manager.List(context.Background(), RunQuery{SessionID: "sess_1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != run.ID || listed[0].TurnID != "turn_1" {
		t.Fatalf("run 목록이 이상해요: %+v", listed)
	}
}

func TestAsyncRunManagerPreservesRequestIDWhenStarterReturnsMetadata(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: map[string]string{"provider": "ok"}}, nil
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "go", Metadata: map[string]string{RequestIDMetadataKey: "req_keep"}})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForRunStatus(t, manager, run.ID, "completed")
	if completed.Metadata[RequestIDMetadataKey] != "req_keep" || completed.Metadata["provider"] != "ok" {
		t.Fatalf("starter metadata와 request id를 함께 보존해야 해요: %+v", completed.Metadata)
	}
}

func TestAsyncRunManagerCancelsRun(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "long"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := manager.Cancel(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelling" && cancelled.Status != "cancelled" {
		t.Fatalf("취소 요청 상태가 이상해요: %+v", cancelled)
	}
	waitForRunStatus(t, manager, run.ID, "cancelled")
}

func TestAsyncRunManagerMarksFailedRun(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return nil, errors.New("boom")
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "fail"})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRunStatus(t, manager, run.ID, "failed")
	if failed.Error == "" {
		t.Fatalf("실패 이유가 필요해요: %+v", failed)
	}
}

func waitForRunStatus(t *testing.T, manager *AsyncRunManager, runID string, status string) *RunDTO {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		run, err := manager.Get(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == status {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := manager.Get(context.Background(), runID)
	t.Fatalf("run 상태가 %s가 되지 않았어요: %+v", status, run)
	return nil
}

func TestAsyncRunManagerPersistsRunState(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", TurnID: "turn_1"}, nil
	}, store)
	run, err := manager.Start(ctx, RunStartRequest{SessionID: sess.ID, Prompt: "go", Provider: "copilot", Model: "gpt-5-mini", MCPServers: []string{"mcp_1"}, Skills: []string{"skill_1"}, Subagents: []string{"agent_1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	loaded := waitForPersistedRunStatus(t, store, run.ID, "completed")
	if loaded.TurnID != "turn_1" || loaded.Provider != "copilot" || loaded.Model != "gpt-5-mini" || len(loaded.MCPServers) != 1 || loaded.MCPServers[0] != "mcp_1" || len(loaded.Skills) != 1 || loaded.Skills[0] != "skill_1" || len(loaded.Subagents) != 1 || loaded.Subagents[0] != "agent_1" {
		t.Fatalf("persisted run이 이상해요: %+v", loaded)
	}
	restarted := NewAsyncRunManagerWithStore(nil, store)
	got, err := restarted.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.TurnID != "turn_1" || got.Provider != "copilot" || got.Model != "gpt-5-mini" || len(got.MCPServers) != 1 || got.MCPServers[0] != "mcp_1" {
		t.Fatalf("restart 후 run 조회가 이상해요: %+v", got)
	}
}

func TestAsyncRunManagerUsesAtomicRunSnapshotStore(t *testing.T) {
	ctx := context.Background()
	store := &atomicRunStore{runs: map[string]session.Run{}}
	manager := NewAsyncRunManagerWithStore(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	}, store)
	run, err := manager.Start(ctx, RunStartRequest{SessionID: "sess_atomic", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	events := waitForRunEventType(t, manager, run.ID, "run.completed")
	plainSaves, plainEvents, snapshotSaves := store.counts()
	if snapshotSaves == 0 {
		t.Fatal("RunSnapshotStore 경로를 써야 해요")
	}
	if plainSaves != 0 || plainEvents != 0 {
		t.Fatalf("SaveRun과 AppendRunEvent를 분리하면 안 돼요: saves=%d events=%d", plainSaves, plainEvents)
	}
	if len(events) == 0 || events[len(events)-1].Type != "run.completed" {
		t.Fatalf("원자 event replay가 이상해요: %+v", events)
	}
}

func TestAsyncRunManagerRecoversStaleRuns(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	stale, err := store.SaveRun(ctx, session.Run{ID: "run_stale", SessionID: sess.ID, Status: "running", Prompt: "go", EventsURL: "/api/v1/sessions/" + sess.ID + "/events"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := store.SaveRun(ctx, session.Run{ID: "run_done", SessionID: sess.ID, Status: "completed", Prompt: "done"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(nil, store)
	if err := manager.RecoverStaleRuns(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.LoadRun(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "failed" || recovered.EndedAt.IsZero() || !strings.Contains(recovered.Error, "gateway restarted") {
		t.Fatalf("stale run 복구가 이상해요: %+v", recovered)
	}
	unchanged, err := store.LoadRun(ctx, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "completed" {
		t.Fatalf("terminal run은 건드리면 안 돼요: %+v", unchanged)
	}
	replay, err := manager.Events(ctx, stale.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) == 0 || replay[len(replay)-1].Type != "run.failed" {
		t.Fatalf("stale recovery event가 필요해요: %+v", replay)
	}
}

func waitForPersistedRunStatus(t *testing.T, store session.RunStore, runID string, status string) session.Run {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		run, err := store.LoadRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == status {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := store.LoadRun(context.Background(), runID)
	t.Fatalf("persisted run 상태가 %s가 되지 않았어요: %+v", status, run)
	return session.Run{}
}

func waitForRunEventType(t *testing.T, manager *AsyncRunManager, runID string, eventType string) []RunEventDTO {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		replay, err := manager.Events(context.Background(), runID, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(replay) > 0 && replay[len(replay)-1].Type == eventType {
			return replay
		}
		time.Sleep(10 * time.Millisecond)
	}
	replay, _ := manager.Events(context.Background(), runID, 0, 10)
	t.Fatalf("run event type %s가 기록되지 않았어요: %+v", eventType, replay)
	return nil
}

func TestRunEventBusPublishesRunUpdates(t *testing.T) {
	bus := NewRunEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsubscribe := bus.Subscribe(ctx, "run_1")
	defer unsubscribe()
	bus.Publish(RunDTO{ID: "run_1", Status: "running"})
	select {
	case run := <-ch:
		if run.Status != "running" {
			t.Fatalf("run event가 이상해요: %+v", run)
		}
	case <-time.After(time.Second):
		t.Fatal("run event를 받지 못했어요")
	}
}

func TestAsyncRunManagerReplaysPersistedRunEvents(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	}, store)
	run, err := manager.Start(ctx, RunStartRequest{SessionID: sess.ID, Prompt: "go", Metadata: map[string]string{RequestIDMetadataKey: "req_async"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	replay := waitForRunEventType(t, manager, run.ID, "run.completed")
	if len(replay) < 3 || replay[0].Type != "run.queued" || replay[len(replay)-1].Type != "run.completed" {
		t.Fatalf("run event replay가 이상해요: %+v", replay)
	}
	for _, event := range replay {
		if event.Run.Metadata[RequestIDMetadataKey] != "req_async" {
			t.Fatalf("run event에 request id metadata가 유지돼야 해요: %+v", event)
		}
	}
	restarted := NewAsyncRunManagerWithStore(nil, store)
	afterFirst, err := restarted.Events(ctx, run.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFirst) == 0 || afterFirst[0].Seq <= 1 {
		t.Fatalf("restart 후 after_seq replay가 이상해요: %+v", afterFirst)
	}
}

type atomicRunStore struct {
	mu            sync.Mutex
	runs          map[string]session.Run
	events        []session.RunEvent
	plainSaves    int
	plainEvents   int
	snapshotSaves int
}

func (s *atomicRunStore) SaveRun(ctx context.Context, run session.Run) (session.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainSaves++
	s.runs[run.ID] = run
	return run, nil
}

func (s *atomicRunStore) LoadRun(ctx context.Context, id string) (session.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return session.Run{}, errors.New("run not found")
	}
	return run, nil
}

func (s *atomicRunStore) ListRuns(ctx context.Context, q session.RunQuery) ([]session.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.Run, 0, len(s.runs))
	for _, run := range s.runs {
		out = append(out, run)
	}
	return out, nil
}

func (s *atomicRunStore) AppendRunEvent(ctx context.Context, event session.RunEvent) (session.RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainEvents++
	event.Seq = len(s.events) + 1
	s.events = append(s.events, event)
	return event, nil
}

func (s *atomicRunStore) ListRunEvents(ctx context.Context, q session.RunEventQuery) ([]session.RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []session.RunEvent{}
	for _, event := range s.events {
		if event.RunID == q.RunID && event.Seq > q.AfterSeq {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *atomicRunStore) SaveRunWithEvent(ctx context.Context, run session.Run, event session.RunEvent) (session.Run, session.RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotSaves++
	s.runs[run.ID] = run
	event.RunID = run.ID
	event.Run = run
	event.Seq = len(s.events) + 1
	s.events = append(s.events, event)
	return run, event, nil
}

func (s *atomicRunStore) counts() (plainSaves int, plainEvents int, snapshotSaves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plainSaves, s.plainEvents, s.snapshotSaves
}
