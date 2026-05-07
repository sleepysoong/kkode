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
	SessionID      string
	Status         string
	RequestID      string
	IdempotencyKey string
	Limit          int
}

// RunGetter는 run 상세 조회 경계예요.
type RunGetter func(ctx context.Context, runID string) (*RunDTO, error)

// RunLister는 run 목록 조회 경계예요.
type RunLister func(ctx context.Context, q RunQuery) ([]RunDTO, error)

// RunCanceler는 실행 중인 background run을 멈추는 경계예요.
type RunCanceler func(ctx context.Context, runID string) (*RunDTO, error)

// RunEventLister는 durable run event replay 경계예요.
type RunEventLister func(ctx context.Context, runID string, afterSeq int, limit int) ([]RunEventDTO, error)

// RunRuntimeStats는 현재 gateway 프로세스가 소유한 background run 실행면 상태예요.
type RunRuntimeStats struct {
	TrackedRuns       int
	ActiveRuns        int
	QueuedRuns        int
	RunningRuns       int
	CancellingRuns    int
	TerminalRuns      int
	MaxConcurrentRuns int
	OccupiedRunSlots  int
	AvailableRunSlots int
	RunTimeout        time.Duration
}

// AsyncRunManager는 HTTP 요청과 실제 agent 실행을 분리하는 in-memory background run 관리자예요.
// run 결과의 원본 상태는 session/event SQLite에 남기고, 이 구조체는 gateway 프로세스 안의 실행 제어면을 맡아요.
type AsyncRunManager struct {
	starter       RunStarter
	runStore      session.RunStore
	runEventStore session.RunEventStore
	eventBus      *RunEventBus
	now           func() time.Time
	runSlots      chan struct{}
	maxConcurrent int
	runTimeout    time.Duration

	mu   sync.RWMutex
	runs map[string]*managedRun
	wg   sync.WaitGroup
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
	return NewAsyncRunManagerWithStoreAndBus(starter, store, NewRunEventBus())
}

func NewAsyncRunManagerWithStoreAndBus(starter RunStarter, store session.RunStore, bus *RunEventBus) *AsyncRunManager {
	if bus == nil {
		bus = NewRunEventBus()
	}
	manager := &AsyncRunManager{starter: starter, runStore: store, eventBus: bus, now: func() time.Time { return time.Now().UTC() }, runs: map[string]*managedRun{}}
	if eventStore, ok := store.(session.RunEventStore); ok {
		manager.runEventStore = eventStore
	}
	return manager
}

// SetMaxConcurrentRuns는 동시에 running 상태로 진입할 background run 수를 제한해요.
// 0 이하이면 제한 없이 기존처럼 실행해요. 보통 gateway 시작 직후 run 접수 전에 설정해요.
func (m *AsyncRunManager) SetMaxConcurrentRuns(max int) *AsyncRunManager {
	if m == nil {
		return m
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxConcurrent = max
	if max > 0 {
		m.runSlots = make(chan struct{}, max)
	} else {
		m.runSlots = nil
	}
	return m
}

func (m *AsyncRunManager) MaxConcurrentRuns() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxConcurrent
}

// SetRunTimeout은 running 상태로 들어간 background run의 최대 실행 시간을 제한해요.
// 0 이하이면 timeout 없이 provider/tool context에 맡겨요.
func (m *AsyncRunManager) SetRunTimeout(timeout time.Duration) *AsyncRunManager {
	if m == nil {
		return m
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runTimeout = timeout
	return m
}

func (m *AsyncRunManager) RunTimeout() time.Duration {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runTimeout
}

// RuntimeStats는 dashboard/diagnostics가 현재 process-local run queue 상태를 읽게 해요.
func (m *AsyncRunManager) RuntimeStats() RunRuntimeStats {
	if m == nil {
		return RunRuntimeStats{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := RunRuntimeStats{
		TrackedRuns:       len(m.runs),
		MaxConcurrentRuns: m.maxConcurrent,
		RunTimeout:        m.runTimeout,
	}
	if m.runSlots != nil {
		stats.OccupiedRunSlots = len(m.runSlots)
		stats.AvailableRunSlots = cap(m.runSlots) - len(m.runSlots)
	}
	for _, managed := range m.runs {
		switch managed.run.Status {
		case "queued":
			stats.QueuedRuns++
		case "running":
			stats.RunningRuns++
		case "cancelling":
			stats.CancellingRuns++
		default:
			if isTerminalRunStatus(managed.run.Status) {
				stats.TerminalRuns++
			} else {
				stats.ActiveRuns++
			}
		}
	}
	stats.ActiveRuns += stats.QueuedRuns + stats.RunningRuns + stats.CancellingRuns
	return stats
}

func (m *AsyncRunManager) Subscribe(ctx context.Context, runID string) (<-chan RunDTO, func()) {
	if m == nil || m.eventBus == nil {
		return NewRunEventBus().Subscribe(ctx, runID)
	}
	return m.eventBus.Subscribe(ctx, runID)
}

func (m *AsyncRunManager) SubscribeEvents(ctx context.Context, runID string) (<-chan RunEventDTO, func()) {
	if m == nil || m.eventBus == nil {
		return NewRunEventBus().SubscribeEvents(ctx, runID)
	}
	return m.eventBus.SubscribeEvents(ctx, runID)
}

// RecoverStaleRuns는 gateway 재시작으로 소유자가 사라진 queued/running/cancelling run을 failed로 닫아요.
// 외부 패널이 영원히 도는 run을 보지 않도록 서버 시작 시 한 번 호출하면 돼요.
func (m *AsyncRunManager) RecoverStaleRuns(ctx context.Context) error {
	if m == nil || m.runStore == nil {
		return nil
	}
	statuses := []string{"queued", "running", "cancelling"}
	for _, status := range statuses {
		runs, err := m.runStore.ListRuns(ctx, session.RunQuery{Status: status, Limit: 1000})
		if err != nil {
			return err
		}
		for _, run := range runs {
			if isTerminalRunStatus(run.Status) {
				continue
			}
			run.Status = "failed"
			run.EndedAt = m.timestamp()
			run.Error = "gateway restarted before this run completed"
			dto := runDTOFromSession(run)
			if err := m.persist(ctx, dto); err != nil {
				return err
			}
			m.publish(*dto)
		}
	}
	return nil
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
	accepted := RunDTO{ID: runID, SessionID: req.SessionID, Prompt: req.Prompt, Provider: req.Provider, Model: req.Model, MCPServers: cloneStringSlice(req.MCPServers), Skills: cloneStringSlice(req.Skills), Subagents: cloneStringSlice(req.Subagents), Status: "queued", EventsURL: runEventsURL(runID), StartedAt: m.timestamp(), Metadata: cloneMap(req.Metadata)}
	m.mu.Lock()
	if existing, exists := m.runs[runID]; exists {
		if sameIdempotencyKey(existing.run.Metadata, req.Metadata) {
			run := markIdempotencyReused(cloneRun(&existing.run))
			m.mu.Unlock()
			cancel()
			return run, nil
		}
		m.mu.Unlock()
		cancel()
		return nil, errors.New("run id가 이미 존재해요")
	}
	m.runs[runID] = &managedRun{run: accepted, cancel: cancel}
	m.mu.Unlock()
	claimed, err := m.claimAcceptedRun(ctx, &accepted)
	if err != nil {
		cancel()
		m.mu.Lock()
		delete(m.runs, runID)
		m.mu.Unlock()
		return nil, err
	}
	if !claimed {
		cancel()
		m.mu.Lock()
		delete(m.runs, runID)
		m.mu.Unlock()
		return markIdempotencyReused(cloneRun(&accepted)), nil
	}
	m.publish(accepted)
	m.wg.Add(1)
	go m.execute(runCtx, cancel, req.withRunID(runID))
	return cloneRun(&accepted), nil
}

func (m *AsyncRunManager) claimAcceptedRun(ctx context.Context, run *RunDTO) (bool, error) {
	if m.runStore == nil {
		return true, nil
	}
	snapshot := sessionRunFromDTO(*run)
	if claimStore, ok := m.runStore.(session.RunClaimStore); ok && m.runEventStore != nil {
		saved, _, claimed, err := claimStore.ClaimRunWithEvent(ctx, snapshot, session.RunEvent{RunID: snapshot.ID, Type: runEventType(snapshot.Status), At: m.timestamp(), Run: snapshot})
		if err != nil {
			return false, err
		}
		*run = *runDTOFromSession(saved)
		return claimed, nil
	}
	if err := m.persist(ctx, run); err != nil {
		return false, err
	}
	return true, nil
}

func sameIdempotencyKey(existing, requested map[string]string) bool {
	key := strings.TrimSpace(requested[IdempotencyMetadataKey])
	return key != "" && strings.TrimSpace(existing[IdempotencyMetadataKey]) == key
}

func markIdempotencyReused(run *RunDTO) *RunDTO {
	if run == nil {
		return nil
	}
	if run.Metadata == nil {
		run.Metadata = map[string]string{}
	}
	run.Metadata[IdempotencyReusedMetadataKey] = "true"
	return run
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
		runs, err := m.runStore.ListRuns(ctx, session.RunQuery{SessionID: q.SessionID, Status: q.Status, RequestID: q.RequestID, IdempotencyKey: q.IdempotencyKey, Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]RunDTO, 0, len(runs))
		for _, run := range runs {
			dto := runDTOFromSession(run)
			if runMatchesQuery(*dto, q) {
				out = append(out, *dto)
			}
		}
		return out, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RunDTO, 0, len(m.runs))
	for _, managed := range m.runs {
		run := managed.run
		if runMatchesQuery(run, q) {
			out = append(out, *cloneRun(&run))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func runMatchesQuery(run RunDTO, q RunQuery) bool {
	if q.SessionID != "" && run.SessionID != q.SessionID {
		return false
	}
	if q.Status != "" && run.Status != q.Status {
		return false
	}
	if q.RequestID != "" && run.Metadata[RequestIDMetadataKey] != q.RequestID {
		return false
	}
	if q.IdempotencyKey != "" && run.Metadata[IdempotencyMetadataKey] != q.IdempotencyKey {
		return false
	}
	return true
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
				dto := runDTOFromSession(run)
				if saveErr := m.persist(ctx, dto); saveErr != nil {
					return nil, saveErr
				}
				return dto, nil
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
	m.publish(run)
	if cancel != nil {
		cancel()
	}
	return &run, nil
}

// Shutdown은 gateway가 종료될 때 소유 중인 active run을 취소하고 최종 상태 저장을 기다려요.
func (m *AsyncRunManager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	type ownedRun struct {
		run    RunDTO
		cancel context.CancelFunc
	}
	var owned []ownedRun
	m.mu.Lock()
	for _, managed := range m.runs {
		if isTerminalRunStatus(managed.run.Status) {
			continue
		}
		managed.run.Status = "cancelling"
		managed.run.EndedAt = m.timestamp()
		managed.run.Error = "gateway shutting down"
		owned = append(owned, ownedRun{run: *cloneRun(&managed.run), cancel: managed.cancel})
	}
	m.mu.Unlock()
	for _, item := range owned {
		run := item.run
		_ = m.persist(ctx, &run)
		m.publish(run)
		if item.cancel != nil {
			item.cancel()
		}
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *AsyncRunManager) execute(ctx context.Context, cancel context.CancelFunc, req RunStartRequest) {
	defer cancel()
	defer m.wg.Done()
	release, acquired := m.acquireRunSlot(ctx)
	if !acquired {
		m.update(req.RunID, func(run *RunDTO) {
			run.Status = "cancelled"
			run.EndedAt = m.timestamp()
			run.Error = ctx.Err().Error()
		})
		return
	}
	defer release()
	runCtx, timeoutCancel := m.withRunTimeout(ctx)
	defer timeoutCancel()
	m.update(req.RunID, func(run *RunDTO) {
		run.Status = "running"
		run.StartedAt = m.timestamp()
	})
	runCtx = WithRunEventReporter(runCtx, func(ctx context.Context, event RunEventDTO) {
		m.recordProgressEvent(ctx, req.RunID, event)
	})
	result, err := m.starter(runCtx, req)
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
		if run.Provider == "" {
			run.Provider = req.Provider
		}
		if run.Model == "" {
			run.Model = req.Model
		}
		if len(run.MCPServers) == 0 {
			run.MCPServers = cloneStringSlice(req.MCPServers)
		}
		if len(run.Skills) == 0 {
			run.Skills = cloneStringSlice(req.Skills)
		}
		if len(run.Subagents) == 0 {
			run.Subagents = cloneStringSlice(req.Subagents)
		}
		run.Metadata = withRequestIDMetadata(run.Metadata, req.Metadata[RequestIDMetadataKey])
		run.EventsURL = runEventsURL(run.ID)
		if run.EndedAt.IsZero() {
			run.EndedAt = m.timestamp()
		}
		if runCtx.Err() != nil {
			if run.Error == "" {
				run.Error = runCtx.Err().Error()
			}
			run.Status = "cancelled"
			return
		}
		if err != nil {
			run.Error = err.Error()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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

func (m *AsyncRunManager) withRunTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if m == nil {
		return ctx, func() {}
	}
	m.mu.RLock()
	timeout := m.runTimeout
	m.mu.RUnlock()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func (m *AsyncRunManager) acquireRunSlot(ctx context.Context) (func(), bool) {
	if m == nil {
		return func() {}, true
	}
	m.mu.RLock()
	slots := m.runSlots
	m.mu.RUnlock()
	if slots == nil {
		return func() {}, true
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-ctx.Done():
		return func() {}, false
	}
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
	m.publish(run)
}

func (m *AsyncRunManager) publish(run RunDTO) {
	if m != nil && m.eventBus != nil {
		m.eventBus.Publish(run)
	}
}

func (m *AsyncRunManager) persist(ctx context.Context, run *RunDTO) error {
	if run == nil {
		return nil
	}
	if m.runStore != nil {
		snapshot := sessionRunFromDTO(*run)
		if snapshotStore, ok := m.runStore.(session.RunSnapshotStore); ok && m.runEventStore != nil {
			saved, _, err := snapshotStore.SaveRunWithEvent(ctx, snapshot, session.RunEvent{RunID: snapshot.ID, Type: runEventType(snapshot.Status), At: m.timestamp(), Run: snapshot})
			if err != nil {
				return err
			}
			*run = *runDTOFromSession(saved)
			return nil
		}
		if _, err := m.runStore.SaveRun(ctx, snapshot); err != nil {
			return err
		}
	}
	return m.recordEvent(ctx, run)
}

func (m *AsyncRunManager) recordEvent(ctx context.Context, run *RunDTO) error {
	if m == nil || m.runEventStore == nil || run == nil || run.ID == "" {
		return nil
	}
	_, err := m.runEventStore.AppendRunEvent(ctx, session.RunEvent{RunID: run.ID, Type: runEventType(run.Status), At: m.timestamp(), Run: sessionRunFromDTO(*run)})
	return err
}

func (m *AsyncRunManager) recordProgressEvent(ctx context.Context, runID string, event RunEventDTO) {
	if m == nil || m.runEventStore == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(event.Type) == "" {
		return
	}
	m.mu.RLock()
	managed, ok := m.runs[runID]
	if !ok {
		m.mu.RUnlock()
		return
	}
	run := *cloneRun(&managed.run)
	m.mu.RUnlock()
	if event.At.IsZero() {
		event.At = m.timestamp()
	}
	event.Run = run
	saved, err := m.runEventStore.AppendRunEvent(ctx, session.RunEvent{
		RunID:   runID,
		Type:    event.Type,
		At:      event.At,
		Tool:    event.Tool,
		Message: event.Message,
		Error:   event.Error,
		Payload: append([]byte(nil), event.Payload...),
		Run:     sessionRunFromDTO(run),
	})
	if err == nil && m.eventBus != nil {
		event.Seq = saved.Seq
		m.eventBus.PublishEvent(event)
	}
}

func (m *AsyncRunManager) Events(ctx context.Context, runID string, afterSeq int, limit int) ([]RunEventDTO, error) {
	if m == nil {
		return nil, errors.New("run manager가 필요해요")
	}
	if m.runEventStore != nil {
		events, err := m.runEventStore.ListRunEvents(ctx, session.RunEventQuery{RunID: runID, AfterSeq: afterSeq, Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]RunEventDTO, 0, len(events))
		for _, event := range events {
			out = append(out, RunEventDTO{Seq: event.Seq, At: event.At, Type: event.Type, Tool: event.Tool, Message: event.Message, Error: event.Error, Payload: append([]byte(nil), event.Payload...), Run: *runDTOFromSession(event.Run)})
		}
		return out, nil
	}
	run, err := m.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if afterSeq >= 1 {
		return []RunEventDTO{}, nil
	}
	return []RunEventDTO{{Seq: 1, At: m.timestamp(), Type: runEventType(run.Status), Run: *run}}, nil
}

func sessionRunFromDTO(run RunDTO) session.Run {
	return session.Run{ID: run.ID, SessionID: run.SessionID, TurnID: run.TurnID, Status: run.Status, Prompt: run.Prompt, Provider: run.Provider, Model: run.Model, MCPServers: cloneStringSlice(run.MCPServers), Skills: cloneStringSlice(run.Skills), Subagents: cloneStringSlice(run.Subagents), EventsURL: run.EventsURL, StartedAt: run.StartedAt, EndedAt: run.EndedAt, Error: run.Error, Metadata: cloneMap(run.Metadata)}
}

func runDTOFromSession(run session.Run) *RunDTO {
	return &RunDTO{ID: run.ID, SessionID: run.SessionID, TurnID: run.TurnID, Status: run.Status, Prompt: run.Prompt, Provider: run.Provider, Model: run.Model, MCPServers: cloneStringSlice(run.MCPServers), Skills: cloneStringSlice(run.Skills), Subagents: cloneStringSlice(run.Subagents), EventsURL: run.EventsURL, StartedAt: run.StartedAt, EndedAt: run.EndedAt, Error: run.Error, Metadata: cloneMap(run.Metadata)}
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

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
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
