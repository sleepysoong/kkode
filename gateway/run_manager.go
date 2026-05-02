package gateway

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sleepysoong/kkode/session"
)

// RunQuery는 외부 adapter가 background run 목록을 좁혀 볼 때 쓰는 조건이에요.
type RunQuery struct {
	SessionID string
	Status    string
	Limit     int
}

// RunGetter는 run 상세 조회 경계예요.
type RunGetter func(ctx context.Context, runID string) (*RunDTO, error)

// RunLister는 run 목록 조회 경계예요.
type RunLister func(ctx context.Context, q RunQuery) ([]RunDTO, error)

// RunCanceler는 실행 중인 background run을 멈추는 경계예요.
type RunCanceler func(ctx context.Context, runID string) (*RunDTO, error)

// AsyncRunManager는 HTTP 요청과 실제 agent 실행을 분리하는 in-memory background run 관리자예요.
// run 결과의 원본 상태는 session/event SQLite에 남기고, 이 구조체는 gateway 프로세스 안의 실행 제어면을 맡아요.
type AsyncRunManager struct {
	starter  RunStarter
	runStore session.RunStore
	now      func() time.Time

	mu   sync.RWMutex
	runs map[string]*managedRun
}

type managedRun struct {
	run    RunDTO
	cancel context.CancelFunc
}

// NewAsyncRunManager는 RunStarter를 background 작업으로 실행하는 관리자를 만들어요.
func NewAsyncRunManager(starter RunStarter) *AsyncRunManager {
	return NewAsyncRunManagerWithStore(starter, nil)
}

func NewAsyncRunManagerWithStore(starter RunStarter, store session.RunStore) *AsyncRunManager {
	return &AsyncRunManager{starter: starter, runStore: store, now: func() time.Time { return time.Now().UTC() }, runs: map[string]*managedRun{}}
}

// Start는 run을 접수한 뒤 goroutine에서 실제 agent 실행을 진행해요.
func (m *AsyncRunManager) Start(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
	if m == nil || m.starter == nil {
		return nil, errors.New("run starter가 필요해요")
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = session.NewID("run")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	accepted := RunDTO{ID: runID, SessionID: req.SessionID, Prompt: req.Prompt, Status: "queued", EventsURL: "/api/v1/sessions/" + req.SessionID + "/events", StartedAt: m.timestamp(), Metadata: cloneMap(req.Metadata)}
	m.mu.Lock()
	if _, exists := m.runs[runID]; exists {
		m.mu.Unlock()
		cancel()
		return nil, errors.New("run id가 이미 존재해요")
	}
	m.runs[runID] = &managedRun{run: accepted, cancel: cancel}
	m.mu.Unlock()
	if err := m.persist(ctx, &accepted); err != nil {
		cancel()
		m.mu.Lock()
		delete(m.runs, runID)
		m.mu.Unlock()
		return nil, err
	}
	go m.execute(runCtx, cancel, req.withRunID(runID))
	return cloneRun(&accepted), nil
}

// Get은 run 상태를 반환해요.
func (m *AsyncRunManager) Get(ctx context.Context, runID string) (*RunDTO, error) {
	if m == nil {
		return nil, errors.New("run manager가 필요해요")
	}
	m.mu.RLock()
	managed, ok := m.runs[runID]
	if ok {
		run := cloneRun(&managed.run)
		m.mu.RUnlock()
		return run, nil
	}
	m.mu.RUnlock()
	if m.runStore != nil {
		run, err := m.runStore.LoadRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		return runDTOFromSession(run), nil
	}
	return nil, errors.New("run을 찾을 수 없어요")
}

// List는 최근 run 목록을 반환해요.
func (m *AsyncRunManager) List(ctx context.Context, q RunQuery) ([]RunDTO, error) {
	if m == nil {
		return nil, errors.New("run manager가 필요해요")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if m.runStore != nil {
		runs, err := m.runStore.ListRuns(ctx, session.RunQuery{SessionID: q.SessionID, Status: q.Status, Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]RunDTO, 0, len(runs))
		for _, run := range runs {
			out = append(out, *runDTOFromSession(run))
		}
		return out, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RunDTO, 0, len(m.runs))
	for _, managed := range m.runs {
		run := managed.run
		if q.SessionID != "" && run.SessionID != q.SessionID {
			continue
		}
		if q.Status != "" && run.Status != q.Status {
			continue
		}
		out = append(out, *cloneRun(&run))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Cancel은 queued/running run의 context를 취소해요.
func (m *AsyncRunManager) Cancel(ctx context.Context, runID string) (*RunDTO, error) {
	if m == nil {
		return nil, errors.New("run manager가 필요해요")
	}
	var cancel context.CancelFunc
	m.mu.Lock()
	managed, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		if m.runStore != nil {
			run, err := m.runStore.LoadRun(ctx, runID)
			if err != nil {
				return nil, err
			}
			if run.Status == "queued" || run.Status == "running" || run.Status == "cancelling" {
				run.Status = "cancelled"
				run.EndedAt = m.timestamp()
				run.Error = "gateway process does not own this run anymore"
				saved, saveErr := m.runStore.SaveRun(ctx, run)
				if saveErr != nil {
					return nil, saveErr
				}
				run = saved
			}
			return runDTOFromSession(run), nil
		}
		return nil, errors.New("run을 찾을 수 없어요")
	}
	if managed.run.Status == "queued" || managed.run.Status == "running" {
		managed.run.Status = "cancelling"
		managed.run.EndedAt = m.timestamp()
		cancel = managed.cancel
	}
	run := *cloneRun(&managed.run)
	m.mu.Unlock()
	_ = m.persist(ctx, &run)
	if cancel != nil {
		cancel()
	}
	return &run, nil
}

func (m *AsyncRunManager) execute(ctx context.Context, cancel context.CancelFunc, req RunStartRequest) {
	defer cancel()
	m.update(req.RunID, func(run *RunDTO) {
		run.Status = "running"
		run.StartedAt = m.timestamp()
	})
	result, err := m.starter(ctx, req)
	m.update(req.RunID, func(run *RunDTO) {
		if result != nil {
			if result.ID == "" {
				result.ID = req.RunID
			}
			if result.ID != req.RunID {
				result.ID = req.RunID
			}
			if result.SessionID == "" {
				result.SessionID = req.SessionID
			}
			if result.Prompt == "" {
				result.Prompt = req.Prompt
			}
			if result.Metadata == nil {
				result.Metadata = cloneMap(req.Metadata)
			}
			*run = *cloneRun(result)
		}
		if run.ID == "" {
			run.ID = req.RunID
		}
		if run.SessionID == "" {
			run.SessionID = req.SessionID
		}
		if run.EventsURL == "" {
			run.EventsURL = "/api/v1/sessions/" + run.SessionID + "/events"
		}
		if run.EndedAt.IsZero() {
			run.EndedAt = m.timestamp()
		}
		if err != nil {
			run.Error = err.Error()
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				run.Status = "cancelled"
			} else {
				run.Status = "failed"
			}
			return
		}
		if run.Status == "" || run.Status == "queued" || run.Status == "running" || run.Status == "cancelling" {
			run.Status = "completed"
		}
	})
}

func (m *AsyncRunManager) update(runID string, fn func(*RunDTO)) {
	m.mu.Lock()
	managed, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return
	}
	fn(&managed.run)
	run := *cloneRun(&managed.run)
	m.mu.Unlock()
	_ = m.persist(context.Background(), &run)
}

func (m *AsyncRunManager) persist(ctx context.Context, run *RunDTO) error {
	if m.runStore == nil || run == nil {
		return nil
	}
	_, err := m.runStore.SaveRun(ctx, sessionRunFromDTO(*run))
	return err
}

func sessionRunFromDTO(run RunDTO) session.Run {
	return session.Run{ID: run.ID, SessionID: run.SessionID, TurnID: run.TurnID, Status: run.Status, Prompt: run.Prompt, EventsURL: run.EventsURL, StartedAt: run.StartedAt, EndedAt: run.EndedAt, Error: run.Error, Metadata: cloneMap(run.Metadata)}
}

func runDTOFromSession(run session.Run) *RunDTO {
	return &RunDTO{ID: run.ID, SessionID: run.SessionID, TurnID: run.TurnID, Status: run.Status, Prompt: run.Prompt, EventsURL: run.EventsURL, StartedAt: run.StartedAt, EndedAt: run.EndedAt, Error: run.Error, Metadata: cloneMap(run.Metadata)}
}

func (m *AsyncRunManager) timestamp() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func (req RunStartRequest) withRunID(runID string) RunStartRequest {
	req.RunID = runID
	return req
}

func cloneRun(run *RunDTO) *RunDTO {
	if run == nil {
		return nil
	}
	out := *run
	out.Metadata = cloneMap(run.Metadata)
	return &out
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
