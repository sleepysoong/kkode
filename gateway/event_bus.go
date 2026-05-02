package gateway

import (
	"context"
	"sync"
)

// RunEventSubscriber는 run 상태 변경을 실시간으로 구독하는 함수예요.
type RunEventSubscriber func(ctx context.Context, runID string) (<-chan RunDTO, func())

// RunEventBus는 프로세스 내부 run 상태 변경을 SSE handler로 전달해요.
type RunEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan RunDTO]struct{}
}

func NewRunEventBus() *RunEventBus {
	return &RunEventBus{subscribers: map[string]map[chan RunDTO]struct{}{}}
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
		select {
		case ch <- *cloneRun(&run):
		default:
		}
	}
}
