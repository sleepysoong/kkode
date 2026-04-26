package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

type ToolRegistry map[string]ToolHandler

func (r ToolRegistry) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	h, ok := r[call.Name]
	if !ok {
		return ToolResult{}, fmt.Errorf("tool %q is not registered", call.Name)
	}
	result, err := h(ctx, call)
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
	MaxIterations int
}

// RunToolLoop executes the Responses-style tool loop: ask the provider, run any
// requested tools, append the provider's output items plus tool outputs, repeat.
// Providers that preserve reasoning items in Response.Output let this loop keep
// reasoning context for models that require it.
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
		for _, call := range resp.ToolCalls {
			result, execErr := tools.Execute(ctx, call)
			if execErr != nil && result.Error == "" {
				result = ToolResult{CallID: call.CallID, Name: call.Name, Error: execErr.Error(), Custom: call.Custom}
			}
			current.InputItems = append(current.InputItems, Item{Type: itemTypeForResult(result), ToolResult: &result})
		}
		current.Messages = nil
		current.PreviousResponseID = resp.ID
	}
	return nil, fmt.Errorf("tool loop exceeded %d iterations", max)
}

func itemTypeForResult(result ToolResult) ItemType {
	if result.Custom {
		return ItemCustomToolOutput
	}
	return ItemFunctionOutput
}
