package gateway

import (
	"context"
	"sync"
)

// RunEventSubscriber는 run 상태 변경을 실시간으로 구독하는 함수예요.
type RunEventSubscriber func(ctx context.Context, runID string) (<-chan RunDTO, func())

// RunEventStreamSubscriber는 상태 변경과 agent/tool progress event를 함께 구독하는 함수예요.
type RunEventStreamSubscriber func(ctx context.Context, runID string) (<-chan RunEventDTO, func())

// RunEventBus는 프로세스 내부 run 상태 변경을 SSE handler로 전달해요.
type RunEventBus struct {
	mu               sync.RWMutex
	subscribers      map[string]map[chan RunDTO]struct{}
	eventSubscribers map[string]map[chan RunEventDTO]struct{}
}

func NewRunEventBus() *RunEventBus {
	return &RunEventBus{subscribers: map[string]map[chan RunDTO]struct{}{}, eventSubscribers: map[string]map[chan RunEventDTO]struct{}{}}
}

func (b *RunEventBus) Subscribe(ctx context.Context, runID string) (<-chan RunDTO, func()) {
	if b == nil {
		closed := make(chan RunDTO)
		close(closed)
		return closed, func() {}
	}
	ch := make(chan RunDTO, 16)
	b.mu.Lock()
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = map[chan RunDTO]struct{}{}
	}
	b.subscribers[runID][ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if subs := b.subscribers[runID]; subs != nil {
				delete(subs, ch)
				if len(subs) == 0 {
					delete(b.subscribers, runID)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return ch, unsubscribe
}

func (b *RunEventBus) SubscribeEvents(ctx context.Context, runID string) (<-chan RunEventDTO, func()) {
	if b == nil {
		closed := make(chan RunEventDTO)
		close(closed)
		return closed, func() {}
	}
	ch := make(chan RunEventDTO, 32)
	b.mu.Lock()
	if b.eventSubscribers[runID] == nil {
		b.eventSubscribers[runID] = map[chan RunEventDTO]struct{}{}
	}
	b.eventSubscribers[runID][ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if subs := b.eventSubscribers[runID]; subs != nil {
				delete(subs, ch)
				if len(subs) == 0 {
					delete(b.eventSubscribers, runID)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return ch, unsubscribe
}

func (b *RunEventBus) Publish(run RunDTO) {
	if b == nil || run.ID == "" {
		return
	}
	b.mu.RLock()
	subs := b.subscribers[run.ID]
	channels := make([]chan RunDTO, 0, len(subs))
	for ch := range subs {
		channels = append(channels, ch)
	}
	b.mu.RUnlock()
	for _, ch := range channels {
		publishRunToChannel(ch, run)
	}
	b.PublishEvent(RunEventDTO{Type: runEventType(run.Status), Run: run})
}

func (b *RunEventBus) PublishEvent(event RunEventDTO) {
	if b == nil || event.Run.ID == "" {
		return
	}
	b.mu.RLock()
	subs := b.eventSubscribers[event.Run.ID]
	channels := make([]chan RunEventDTO, 0, len(subs))
	for ch := range subs {
		channels = append(channels, ch)
	}
	b.mu.RUnlock()
	for _, ch := range channels {
		publishEventToChannel(ch, event)
	}
}

func publishRunToChannel(ch chan RunDTO, run RunDTO) {
	defer func() {
		_ = recover()
	}()
	cloned := *cloneRun(&run)
	select {
	case ch <- cloned:
		return
	default:
	}
	if !isTerminalRunStatus(run.Status) {
		return
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- cloned:
	default:
	}
}

func publishEventToChannel(ch chan RunEventDTO, event RunEventDTO) {
	defer func() {
		_ = recover()
	}()
	cloned := event
	cloned.Run = *cloneRun(&event.Run)
	cloned.Payload = append([]byte(nil), event.Payload...)
	select {
	case ch <- cloned:
		return
	default:
	}
	if !isTerminalRunStatus(event.Run.Status) {
		return
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- cloned:
	default:
	}
}
