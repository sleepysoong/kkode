package kruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/prompts"
	"github.com/sleepysoong/kkode/session"
)

type Runtime struct {
	Store           session.Store
	Agent           *agent.Agent
	ProjectRoot     string
	ProviderName    string
	Model           string
	AgentName       string
	Mode            session.AgentMode
	MaxHistoryTurns int
	Compaction      session.CompactionPolicy
	EnableTodos     bool
}

type RunOptions struct {
	SessionID string
	ForkFrom  string
	ForkAt    string
	Prompt    string
}

type RunResult struct {
	Session *session.Session
	Turn    session.Turn
	Agent   *agent.RunResult
}

func (r *Runtime) Run(ctx context.Context, opts RunOptions) (*RunResult, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	if r.Agent == nil {
		return nil, fmt.Errorf("agent is required")
	}
	if strings.TrimSpace(opts.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	sess, err := r.resolveSession(ctx, opts)
	if err != nil {
		return nil, err
	}
	req, handlers := r.Agent.Prepare(opts.Prompt)
	req = r.applySessionContext(sess, req)
	if r.EnableTodos {
		req.Messages = append([]llm.Message{llm.DeveloperText(session.TodoInstructions())}, req.Messages...)
		tools := llm.NewToolSet(req.Tools, handlers)
		tools.Merge(session.TodoToolSet(r.Store, sess.ID))
		req.Tools, handlers = tools.Parts()
	}
	turn := session.NewTurn(opts.Prompt, req)
	r.appendRuntimeEvent(sess, turn.ID, "turn.started", "", nil, "")
	started := time.Now().UTC()
	result, runErr := r.Agent.RunPrepared(ctx, opts.Prompt, req, handlers)
	turn.StartedAt = started
	turn.EndedAt = time.Now().UTC()
	if result != nil {
		turn.Response = result.Response
		for _, ev := range result.Trace {
			r.appendTraceEvent(sess, turn.ID, ev)
		}
	}
	if runErr != nil {
		turn.Error = runErr.Error()
		r.appendRuntimeEvent(sess, turn.ID, "turn.failed", "", nil, runErr.Error())
	} else {
		r.appendRuntimeEvent(sess, turn.ID, "turn.completed", "", nil, "")
	}
	sess.AppendTurn(turn)
	if r.Compaction.Enabled {
		r.maybeCompact(sess)
	}
	if r.EnableTodos {
		if latest, loadErr := r.Store.LoadSession(ctx, sess.ID); loadErr == nil {
			sess.Todos = latest.Todos
		}
	}
	if err := r.Store.SaveSession(ctx, sess); err != nil {
		return nil, err
	}
	return &RunResult{Session: sess, Turn: turn, Agent: result}, runErr
}

func (r *Runtime) Resume(ctx context.Context, sessionID string) (*session.Session, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	return r.Store.LoadSession(ctx, sessionID)
}

func (r *Runtime) Fork(ctx context.Context, sessionID string, atTurnID string) (*session.Session, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	return session.Fork(ctx, r.Store, sessionID, atTurnID)
}

func (r *Runtime) resolveSession(ctx context.Context, opts RunOptions) (*session.Session, error) {
	if opts.ForkFrom != "" {
		return r.Fork(ctx, opts.ForkFrom, opts.ForkAt)
	}
	if opts.SessionID != "" {
		return r.Store.LoadSession(ctx, opts.SessionID)
	}
	sess := session.NewSession(r.ProjectRoot, r.ProviderName, r.Model, firstNonEmpty(r.AgentName, "kkode-agent"), r.Mode)
	if err := r.Store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (r *Runtime) applySessionContext(sess *session.Session, req llm.Request) llm.Request {
	messages := append([]llm.Message{}, req.Messages...)
	history := r.historyMessages(sess)
	if sess.Summary != "" {
		summary, err := prompts.Render(prompts.SessionSummaryContext, map[string]any{"Summary": sess.Summary})
		if err != nil {
			summary = "이전 세션 요약이에요:\n" + sess.Summary
		}
		history = append([]llm.Message{llm.DeveloperText(summary)}, history...)
	}
	if len(history) > 0 {
		req.Messages = append(history, messages...)
	}
	if req.PreviousResponseID == "" && sess.LastResponseID != "" && len(history) == 0 {
		req.PreviousResponseID = sess.LastResponseID
	}
	return req
}

func (r *Runtime) historyMessages(sess *session.Session) []llm.Message {
	if sess == nil || len(sess.Turns) == 0 {
		return nil
	}
	turns := sess.Turns
	if r.MaxHistoryTurns > 0 && len(turns) > r.MaxHistoryTurns {
		turns = turns[len(turns)-r.MaxHistoryTurns:]
	}
	messages := make([]llm.Message, 0, len(turns)*2)
	for _, turn := range turns {
		if strings.TrimSpace(turn.Prompt) != "" {
			messages = append(messages, llm.UserText(turn.Prompt))
		}
		if turn.Response != nil && strings.TrimSpace(turn.Response.Text) != "" {
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: turn.Response.Text})
		}
	}
	return messages
}

func (r *Runtime) maybeCompact(sess *session.Session) {
	threshold := r.Compaction.TriggerTokenRatio
	if threshold <= 0 {
		threshold = 0.85
	}
	if len(sess.Turns) < 6 {
		return
	}
	preserveFirst := r.Compaction.PreserveFirstNTurns
	if preserveFirst < 0 {
		preserveFirst = 0
	}
	preserveLast := r.Compaction.PreserveLastNTurns
	if preserveLast <= 0 {
		preserveLast = 3
	}
	if float64(len(sess.Turns))/20.0 < threshold {
		return
	}
	if summary := session.BuildExtractiveSummary(sess, preserveFirst, preserveLast); summary != "" {
		sess.Summary = summary
	}
}

func (r *Runtime) appendTraceEvent(sess *session.Session, turnID string, ev agent.TraceEvent) {
	payload, _ := json.Marshal(map[string]string{"message": ev.Message})
	r.appendRuntimeEvent(sess, turnID, ev.Type, ev.Tool, payload, ev.Error)
}

func (r *Runtime) appendRuntimeEvent(sess *session.Session, turnID string, typ string, tool string, payload json.RawMessage, errText string) {
	ev := session.NewEvent(sess.ID, turnID, typ)
	ev.Tool = tool
	ev.Payload = payload
	ev.Error = errText
	sess.AppendEvent(ev)
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
