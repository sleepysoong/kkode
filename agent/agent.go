package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/transcript"
	"github.com/sleepysoong/kkode/workspace"
)

type Config struct {
	Name          string
	Provider      llm.Provider
	Model         string
	Instructions  string
	BaseRequest   llm.Request
	Workspace     *workspace.Workspace
	Tools         []llm.Tool
	ToolHandlers  llm.ToolRegistry
	MaxIterations int
	Transcript    *transcript.Transcript
	Observer      Observer
	Guardrails    Guardrails
}

type Agent struct {
	cfg Config
}

type RunResult struct {
	Response *llm.Response
	Trace    []TraceEvent
}

type Observer interface {
	OnEvent(ctx context.Context, event TraceEvent)
}

type ObserverFunc func(ctx context.Context, event TraceEvent)

func (f ObserverFunc) OnEvent(ctx context.Context, event TraceEvent) { f(ctx, event) }

type TraceEvent struct {
	At      time.Time
	Type    string
	Message string
	Tool    string
	Error   string
}

type Guardrails struct {
	BlockedSubstrings       []string
	BlockedOutputSubstrings []string
	RedactTranscript        bool
}

type traceEventsKey struct{}

func New(cfg Config) (*Agent, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if cfg.Name == "" {
		cfg.Name = "kkode-agent"
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 8
	}
	if cfg.ToolHandlers == nil {
		cfg.ToolHandlers = llm.ToolRegistry{}
	}
	return &Agent{cfg: cfg}, nil
}

func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error) {
	req, handlers := a.Prepare(prompt)
	return a.RunPrepared(ctx, prompt, req, handlers)
}

func (a *Agent) Prepare(prompt string) (llm.Request, llm.ToolRegistry) {
	tools, handlers := a.tools()
	return a.request(prompt, tools), handlers
}

func (a *Agent) RunPrepared(ctx context.Context, prompt string, req llm.Request, handlers llm.ToolRegistry) (*RunResult, error) {
	if err := a.cfg.Guardrails.CheckInput(prompt); err != nil {
		a.emit(ctx, TraceEvent{Type: "guardrail.blocked", Error: err.Error()})
		return nil, err
	}
	var trace []TraceEvent
	ctx = context.WithValue(ctx, traceEventsKey{}, &trace)
	a.emit(ctx, TraceEvent{Type: "agent.started", Message: prompt})
	tracedHandlers := a.traceTools(handlers)
	resp, err := llm.RunToolLoop(ctx, a.cfg.Provider, req, tracedHandlers, llm.ToolLoopOptions{MaxIterations: a.cfg.MaxIterations})
	if err != nil {
		a.emit(ctx, TraceEvent{Type: "agent.failed", Error: err.Error()})
	} else if resp == nil {
		err = fmt.Errorf("provider returned nil response")
		a.emit(ctx, TraceEvent{Type: "agent.failed", Error: err.Error()})
	} else if err := a.cfg.Guardrails.CheckOutput(resp.Text); err != nil {
		a.emit(ctx, TraceEvent{Type: "guardrail.output_blocked", Error: err.Error()})
		if a.cfg.Transcript != nil {
			a.cfg.Transcript.Add(req, resp, err)
		}
		return &RunResult{Response: resp, Trace: trace}, err
	} else {
		a.emit(ctx, TraceEvent{Type: "agent.completed", Message: resp.Text})
	}
	if a.cfg.Transcript != nil {
		a.cfg.Transcript.Add(req, resp, err)
	}
	return &RunResult{Response: resp, Trace: trace}, err
}

func (a *Agent) Stream(ctx context.Context, prompt string) (llm.EventStream, error) {
	sp, ok := a.cfg.Provider.(llm.StreamProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s does not support streaming", a.cfg.Provider.Name())
	}
	if err := a.cfg.Guardrails.CheckInput(prompt); err != nil {
		return nil, err
	}
	tools, _ := a.tools()
	return sp.Stream(ctx, a.request(prompt, tools))
}

func (a *Agent) request(prompt string, tools []llm.Tool) llm.Request {
	req := a.cfg.BaseRequest
	req.Model = a.cfg.Model
	if req.Instructions == "" {
		req.Instructions = a.instructions()
	}
	req.Messages = append(append([]llm.Message{}, req.Messages...), llm.UserText(prompt))
	req.Tools = append(append([]llm.Tool{}, req.Tools...), tools...)
	if req.Store == nil {
		req.Store = llm.Bool(false)
	}
	return req
}

func (a *Agent) tools() ([]llm.Tool, llm.ToolRegistry) {
	defs := append([]llm.Tool{}, a.cfg.Tools...)
	handlers := llm.ToolRegistry{}
	for name, handler := range a.cfg.ToolHandlers {
		handlers[name] = handler
	}
	if a.cfg.Workspace != nil {
		workspaceDefs, workspaceHandlers := a.cfg.Workspace.Tools()
		defs = append(defs, workspaceDefs...)
		for name, handler := range workspaceHandlers {
			handlers[name] = handler
		}
	}
	return defs, handlers
}

func (a *Agent) instructions() string {
	if a.cfg.Instructions != "" {
		return a.cfg.Instructions
	}
	return "너는 Go 바이브코딩 에이전트예요. 필요한 경우 file_read/file_write/file_edit/file_apply_patch/file_glob/file_grep/shell_run/web_fetch 같은 tool을 사용해요. 현재 기본은 YOLO 모드라 요청받은 파일 수정과 명령 실행을 적극적으로 수행해요. 최종 답변에는 수행한 작업과 검증 결과를 짧게 정리해요."
}

func (a *Agent) traceTools(reg llm.ToolRegistry) llm.ToolRegistry {
	out := llm.ToolRegistry{}
	for name, handler := range reg {
		name := name
		handler := handler
		out[name] = func(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
			a.emit(ctx, TraceEvent{Type: "tool.started", Tool: name})
			res, err := handler(ctx, call)
			if err != nil {
				a.emit(ctx, TraceEvent{Type: "tool.failed", Tool: name, Error: err.Error()})
				return res, err
			}
			a.emit(ctx, TraceEvent{Type: "tool.completed", Tool: name, Message: llm.RedactSecrets(res.Output)})
			return res, nil
		}
	}
	return out
}

func (a *Agent) emit(ctx context.Context, event TraceEvent) {
	event.At = time.Now().UTC()
	if events, ok := ctx.Value(traceEventsKey{}).(*[]TraceEvent); ok && events != nil {
		*events = append(*events, event)
	}
	if a.cfg.Observer != nil {
		a.cfg.Observer.OnEvent(ctx, event)
	}
}
