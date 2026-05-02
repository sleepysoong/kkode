package gateway

import (
	"context"
	"errors"
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
	run, err := manager.Start(ctx, RunStartRequest{SessionID: sess.ID, Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, manager, run.ID, "completed")
	loaded := waitForPersistedRunStatus(t, store, run.ID, "completed")
	if loaded.TurnID != "turn_1" {
		t.Fatalf("persisted run이 이상해요: %+v", loaded)
	}
	restarted := NewAsyncRunManagerWithStore(nil, store)
	got, err := restarted.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.TurnID != "turn_1" {
		t.Fatalf("restart 후 run 조회가 이상해요: %+v", got)
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
