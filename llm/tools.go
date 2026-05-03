package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// ToolMiddleware는 tool 실행 전후 공통 동작을 agent/gateway/provider 표면에서 재사용하게 해요.
type ToolMiddleware func(name string, next ToolHandler) ToolHandler

type ToolRegistry map[string]ToolHandler

// ToolSet은 model에 노출할 tool 정의와 실제 실행 handler를 한 묶음으로 들고 다녀요.
// 같은 이름이 충돌하면 나중에 Merge된 handler가 이겨서 runtime이 명시적으로 덮어쓸 수 있어요.
type ToolSet struct {
	Definitions []Tool
	Handlers    ToolRegistry
}

// NewToolSet은 외부 slice/map을 방어 복사해서 재사용 가능한 tool 묶음을 만들어요.
func NewToolSet(defs []Tool, handlers ToolRegistry) ToolSet {
	set := ToolSet{}
	set.Definitions = append(set.Definitions, defs...)
	if len(handlers) > 0 {
		set.Handlers = ToolRegistry{}
		for name, handler := range handlers {
			set.Handlers[name] = handler
		}
	}
	return set
}

// Clone은 caller가 안전하게 수정할 수 있는 복사본을 돌려줘요.
func (s ToolSet) Clone() ToolSet { return NewToolSet(s.Definitions, s.Handlers) }

// Merge는 다른 tool 묶음을 현재 묶음 뒤에 붙여요. handler 이름 충돌은 뒤쪽 묶음이 이겨요.
func (s *ToolSet) Merge(other ToolSet) {
	s.Definitions = append(s.Definitions, other.Definitions...)
	if len(other.Handlers) == 0 {
		return
	}
	if s.Handlers == nil {
		s.Handlers = ToolRegistry{}
	}
	for name, handler := range other.Handlers {
		s.Handlers[name] = handler
	}
}

// Parts는 기존 API와 연결할 수 있게 정의와 handler registry를 복사해서 분리해요.
func (s ToolSet) Parts() ([]Tool, ToolRegistry) {
	cloned := s.Clone()
	return cloned.Definitions, cloned.Handlers
}

// WithMiddleware는 registry를 복사한 뒤 각 handler에 middleware chain을 감싸요.
// 먼저 넘긴 middleware가 가장 바깥에서 실행돼서 tracing/timeout/metric 순서를 읽기 쉽게 유지해요.
func (r ToolRegistry) WithMiddleware(middlewares ...ToolMiddleware) ToolRegistry {
	out := ToolRegistry{}
	for name, handler := range r {
		wrapped := handler
		for i := len(middlewares) - 1; i >= 0; i-- {
			if middlewares[i] == nil {
				continue
			}
			wrapped = middlewares[i](name, wrapped)
		}
		out[name] = wrapped
	}
	return out
}

func (r ToolRegistry) Execute(ctx context.Context, call ToolCall) (result ToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool %q panic: %v", call.Name, recovered)
			result = ToolResult{CallID: call.CallID, Name: call.Name, Error: err.Error(), Custom: call.Custom}
		}
	}()
	h, ok := r[call.Name]
	if !ok {
		return ToolResult{}, fmt.Errorf("tool %q is not registered", call.Name)
	}
	if h == nil {
		return ToolResult{}, fmt.Errorf("tool %q handler is nil", call.Name)
	}
	result, err = h(ctx, call)
	if err != nil {
		return ToolResult{CallID: call.CallID, Name: call.Name, Error: err.Error(), Custom: call.Custom}, err
	}
	if result.CallID == "" {
		result.CallID = call.CallID
	}
	if result.Name == "" {
		result.Name = call.Name
	}
	result.Custom = call.Custom
	return result, nil
}

func JSONToolHandler[T any](fn func(context.Context, T) (string, error)) ToolHandler {
	return func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var in T
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &in); err != nil {
				return ToolResult{}, fmt.Errorf("decode %s arguments: %w", call.Name, err)
			}
		}
		out, err := fn(ctx, in)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{CallID: call.CallID, Name: call.Name, Output: out, Custom: call.Custom}, nil
	}
}

type ToolLoopOptions struct {
	MaxIterations        int
	ParallelToolCalls    bool
	MaxParallelToolCalls int
}

// RunToolLoop는 Responses-style tool loop를 실행해요.
// provider를 호출하고, 요청된 tool을 실행하고, provider output item과 tool output을 붙인 뒤 반복해요.
// Response.Output에 reasoning item을 보존하는 provider라면 reasoning context도 계속 유지할 수 있어요.
func RunToolLoop(ctx context.Context, p Provider, req Request, tools ToolRegistry, opts ToolLoopOptions) (*Response, error) {
	max := opts.MaxIterations
	if max <= 0 {
		max = 8
	}
	current := req
	for i := 0; i < max; i++ {
		resp, err := p.Generate(ctx, current)
		if err != nil {
			return nil, err
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		current.InputItems = append(current.InputItems, resp.Output...)
		results := executeToolCalls(ctx, resp.ToolCalls, tools, opts.ParallelToolCalls, opts.MaxParallelToolCalls)
		for _, result := range results {
			current.InputItems = append(current.InputItems, Item{Type: itemTypeForResult(result), ToolResult: &result})
		}
		current.Messages = nil
		current.PreviousResponseID = resp.ID
	}
	return nil, fmt.Errorf("tool loop exceeded %d iterations", max)
}

func executeToolCalls(ctx context.Context, calls []ToolCall, tools ToolRegistry, parallel bool, maxParallel int) []ToolResult {
	results := make([]ToolResult, len(calls))
	if !parallel || len(calls) < 2 {
		for i, call := range calls {
			results[i] = executeToolCall(ctx, tools, call)
		}
		return results
	}
	if maxParallel <= 0 {
		maxParallel = 4
	}
	if maxParallel > len(calls) {
		maxParallel = len(calls)
	}
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, call := range calls {
		i, call := i, call
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = ToolResult{CallID: call.CallID, Name: call.Name, Error: ctx.Err().Error(), Custom: call.Custom}
				return
			}
			results[i] = executeToolCall(ctx, tools, call)
		}()
	}
	wg.Wait()
	return results
}

func executeToolCall(ctx context.Context, tools ToolRegistry, call ToolCall) (result ToolResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = ToolResult{CallID: call.CallID, Name: call.Name, Error: fmt.Sprintf("tool %q panic: %v", call.Name, recovered), Custom: call.Custom}
		}
	}()
	result, execErr := tools.Execute(ctx, call)
	if execErr != nil && result.Error == "" {
		result = ToolResult{CallID: call.CallID, Name: call.Name, Error: execErr.Error(), Custom: call.Custom}
	}
	return result
}

func itemTypeForResult(result ToolResult) ItemType {
	if result.Custom {
		return ItemCustomToolOutput
	}
	return ItemFunctionOutput
}
