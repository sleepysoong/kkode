package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
	if run.ID == "" || run.Status != "queued" || run.EventsURL != runEventsURL(run.ID) {
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

func TestAsyncRunManagerListsRunsByRequestID(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: req.Metadata}, nil
	})
	first, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "one", Metadata: map[string]string{RequestIDMetadataKey: "req_one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "two", Metadata: map[string]string{RequestIDMetadataKey: "req_two"}}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, first.ID, "completed")
	listed, err := manager.List(context.Background(), RunQuery{RequestID: "req_one", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID || listed[0].Metadata[RequestIDMetadataKey] != "req_one" {
		t.Fatalf("request_id run 목록이 이상해요: %+v", listed)
	}
}

func TestAsyncRunManagerListsRunsByIdempotencyKey(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: req.Metadata}, nil
	})
	first, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "one", Metadata: map[string]string{IdempotencyMetadataKey: "idem_one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "two", Metadata: map[string]string{IdempotencyMetadataKey: "idem_two"}}); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, first.ID, "completed")
	listed, err := manager.List(context.Background(), RunQuery{IdempotencyKey: "idem_one", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID || listed[0].Metadata[IdempotencyMetadataKey] != "idem_one" {
		t.Fatalf("idempotency_key run 목록이 이상해요: %+v", listed)
	}
}

func TestAsyncRunManagerReusesInMemoryIdempotentRun(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	var starts int32
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		atomic.AddInt32(&starts, 1)
		started <- struct{}{}
		<-block
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: req.Metadata}, nil
	})
	defer close(block)
	req := RunStartRequest{RunID: "run_idem_test", SessionID: "sess_1", Prompt: "go", Metadata: map[string]string{IdempotencyMetadataKey: "idem_same"}}
	first, err := manager.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := manager.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Metadata[IdempotencyReusedMetadataKey] != "true" || atomic.LoadInt32(&starts) != 1 {
		t.Fatalf("같은 in-memory idempotent run은 재사용해야 해요: first=%+v second=%+v starts=%d", first, second, starts)
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

func TestAsyncRunManagerKeepsCancelledStatusWhenStarterReturnsSuccessAfterCancel(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		<-ctx.Done()
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "ignore cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitForRunStatus(t, manager, run.ID, "cancelled")
	if cancelled.Error == "" {
		t.Fatalf("context 취소 이유가 남아야 해요: %+v", cancelled)
	}
}

func TestAsyncRunManagerLimitsConcurrentRunningRuns(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 2)
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		started <- req.RunID
		<-release
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	}).SetMaxConcurrentRuns(1)
	first, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}
	var runningID string
	select {
	case runningID = <-started:
	case <-time.After(time.Second):
		t.Fatal("첫 run은 시작해야 해요")
	}
	queuedID := second.ID
	if runningID == second.ID {
		queuedID = first.ID
	}
	if queued, err := manager.Get(context.Background(), queuedID); err != nil || queued.Status != "queued" {
		t.Fatalf("다른 run은 slot이 빌 때까지 queued여야 해요: run=%+v err=%v", queued, err)
	}
	select {
	case id := <-started:
		t.Fatalf("slot이 꽉 찼는데 두 번째 run이 시작되면 안 돼요: %s", id)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	waitForRunStatus(t, manager, first.ID, "completed")
	waitForRunStatus(t, manager, second.ID, "completed")
	if manager.MaxConcurrentRuns() != 1 {
		t.Fatalf("concurrency limit가 유지돼야 해요: %d", manager.MaxConcurrentRuns())
	}
}

func TestAsyncRunManagerReportsRuntimeStats(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 1)
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		started <- req.RunID
		<-release
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	}).SetMaxConcurrentRuns(1).SetRunTimeout(time.Minute)
	first, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stats := manager.RuntimeStats()
	if stats.TrackedRuns != 2 || stats.ActiveRuns != 2 || stats.RunningRuns != 1 || stats.QueuedRuns != 1 || stats.MaxConcurrentRuns != 1 || stats.OccupiedRunSlots != 1 || stats.AvailableRunSlots != 0 || stats.RunTimeout != time.Minute {
		t.Fatalf("runtime stats가 이상해요: %+v", stats)
	}
	close(release)
	waitForRunStatus(t, manager, first.ID, "completed")
	waitForRunStatus(t, manager, second.ID, "completed")
	stats = manager.RuntimeStats()
	if stats.ActiveRuns != 0 || stats.TerminalRuns != 2 {
		t.Fatalf("terminal stats가 이상해요: %+v", stats)
	}
}

func TestAsyncRunManagerCancelsTimedOutRun(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}).SetRunTimeout(10 * time.Millisecond)
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := waitForRunStatus(t, manager, run.ID, "cancelled")
	if !strings.Contains(cancelled.Error, "deadline") {
		t.Fatalf("timeout 이유가 남아야 해요: %+v", cancelled)
	}
	if manager.RunTimeout() != 10*time.Millisecond {
		t.Fatalf("run timeout이 유지돼야 해요: %s", manager.RunTimeout())
	}
}

func TestAsyncRunManagerShutdownCancelsActiveRuns(t *testing.T) {
	manager := NewAsyncRunManager(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	run, err := manager.Start(context.Background(), RunStartRequest{SessionID: "sess_1", Prompt: "long"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancelled, err := manager.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || cancelled.Error == "" {
		t.Fatalf("shutdown은 active run을 취소 상태로 남겨야 해요: %+v", cancelled)
	}
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
	run, err := manager.Start(ctx, RunStartRequest{SessionID: sess.ID, Prompt: "go", Provider: "copilot", Model: "gpt-5-mini", MCPServers: []string{"mcp_1"}, Skills: []string{"skill_1"}, Subagents: []string{"agent_1"}, EnabledTools: []string{"file_read"}, DisabledTools: []string{"shell_run"}, ContextBlocks: []string{"adapter token=ghp_123456789012345678901234567890123456 context"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	loaded := waitForPersistedRunStatus(t, store, run.ID, "completed")
	if loaded.TurnID != "turn_1" || loaded.Provider != "copilot" || loaded.Model != "gpt-5-mini" || len(loaded.MCPServers) != 1 || loaded.MCPServers[0] != "mcp_1" || len(loaded.Skills) != 1 || loaded.Skills[0] != "skill_1" || len(loaded.Subagents) != 1 || loaded.Subagents[0] != "agent_1" || len(loaded.EnabledTools) != 1 || loaded.EnabledTools[0] != "file_read" || len(loaded.DisabledTools) != 1 || loaded.DisabledTools[0] != "shell_run" || len(loaded.ContextBlocks) != 1 || strings.Contains(loaded.ContextBlocks[0], "ghp_") || !strings.Contains(loaded.ContextBlocks[0], "[REDACTED]") {
		t.Fatalf("persisted run이 이상해요: %+v", loaded)
	}
	restarted := NewAsyncRunManagerWithStore(nil, store)
	got, err := restarted.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.TurnID != "turn_1" || got.Provider != "copilot" || got.Model != "gpt-5-mini" || len(got.MCPServers) != 1 || got.MCPServers[0] != "mcp_1" || len(got.EnabledTools) != 1 || got.EnabledTools[0] != "file_read" || len(got.DisabledTools) != 1 || got.DisabledTools[0] != "shell_run" || len(got.ContextBlocks) != 1 || strings.Contains(got.ContextBlocks[0], "ghp_") || !strings.Contains(got.ContextBlocks[0], "[REDACTED]") {
		t.Fatalf("restart 후 run 조회가 이상해요: %+v", got)
	}
}

func TestAsyncRunManagerUsesDurableClaimForDuplicateRunID(t *testing.T) {
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
	block := make(chan struct{})
	defer close(block)
	var starts int32
	firstManager := NewAsyncRunManagerWithStore(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		atomic.AddInt32(&starts, 1)
		<-block
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: req.Metadata}, nil
	}, store)
	req := RunStartRequest{RunID: "run_idem_durable", SessionID: sess.ID, Prompt: "go", Metadata: map[string]string{IdempotencyMetadataKey: "idem_durable"}}
	first, err := firstManager.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	secondManager := NewAsyncRunManagerWithStore(func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
		atomic.AddInt32(&starts, 1)
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed", Metadata: req.Metadata}, nil
	}, store)
	second, err := secondManager.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Metadata[IdempotencyReusedMetadataKey] != "true" || atomic.LoadInt32(&starts) > 1 {
		t.Fatalf("durable claim이 있으면 두 번째 manager는 실행하지 않아야 해요: first=%+v second=%+v starts=%d", first, second, starts)
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
	stale, err := store.SaveRun(ctx, session.Run{ID: "run_stale", SessionID: sess.ID, Status: "running", Prompt: "go", EventsURL: runEventsURL("run_stale")})
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

func TestRunEventBusPublishesProgressEvents(t *testing.T) {
	bus := NewRunEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsubscribe := bus.SubscribeEvents(ctx, "run_1")
	defer unsubscribe()
	bus.PublishEvent(RunEventDTO{Type: "tool.completed", Tool: "file_read", Run: RunDTO{ID: "run_1", Status: "running"}})
	select {
	case event := <-ch:
		if event.Type != "tool.completed" || event.Tool != "file_read" || event.Run.ID != "run_1" {
			t.Fatalf("progress event가 이상해요: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("progress event를 받지 못했어요")
	}
}

func TestRunEventBusPreservesTerminalUpdateWhenSubscriberBufferIsFull(t *testing.T) {
	bus := NewRunEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsubscribe := bus.Subscribe(ctx, "run_full")
	defer unsubscribe()
	for i := 0; i < 32; i++ {
		bus.Publish(RunDTO{ID: "run_full", Status: "running"})
	}
	bus.Publish(RunDTO{ID: "run_full", Status: "completed"})
	var sawCompleted bool
	for i := 0; i < 16; i++ {
		select {
		case run := <-ch:
			if run.Status == "completed" {
				sawCompleted = true
			}
		case <-time.After(time.Second):
			t.Fatal("buffered run event를 받지 못했어요")
		}
	}
	if !sawCompleted {
		t.Fatal("subscriber buffer가 꽉 차도 terminal update는 보존해야 해요")
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

func TestAsyncRunManagerRecordsProgressEventsFromRunContext(t *testing.T) {
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
		if ok := ReportRunEvent(ctx, RunEventDTO{Type: "tool.completed", Tool: "file_read", Message: "token=abc1234567890secretvalue"}); !ok {
			t.Fatal("run context에 progress reporter가 필요해요")
		}
		return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "completed"}, nil
	}, store)
	run, err := manager.Start(ctx, RunStartRequest{SessionID: sess.ID, Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	replay := waitForRunEventType(t, manager, run.ID, "run.completed")
	var sawTrace bool
	for _, event := range replay {
		if event.Type == "tool.completed" && event.Tool == "file_read" {
			sawTrace = true
			if event.Run.ID != run.ID || event.Message != "token=abc1234567890secretvalue" {
				t.Fatalf("progress event replay가 이상해요: %+v", event)
			}
		}
	}
	if !sawTrace {
		t.Fatalf("agent/tool progress event가 durable run event에 필요해요: %+v", replay)
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
