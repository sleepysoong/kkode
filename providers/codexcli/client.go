package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sleepysoong/kkode/llm"
)

type Config struct {
	Binary           string
	WorkingDirectory string
	Sandbox          string
	Ephemeral        bool
	ExtraArgs        []string
}

type Client struct{ cfg Config }

func New(cfg Config) *Client {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = "danger-full-access"
	}
	return &Client{cfg: cfg}
}

func (c *Client) Name() string { return "codex-cli" }

func (c *Client) Capabilities() llm.Capabilities {
	return llm.Capabilities{Reasoning: true, Streaming: true, Tools: true, MCP: true, Skills: true}
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	out, err := os.CreateTemp("", "kkode-codex-last-*.txt")
	if err != nil {
		return nil, err
	}
	outPath := out.Name()
	_ = out.Close()
	defer os.Remove(outPath)
	cmd := c.command(ctx, req, "-o", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("codex exec failed: %w: %s", err, stderr.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(data))
	return &llm.Response{Provider: c.Name(), Model: req.Model, Status: "completed", Text: text, Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: text}}}, nil
}

func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	cmd := c.command(ctx, req)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	events := make(chan llm.StreamEvent, 32)
	closer := &processCloser{cmd: cmd, stdout: stdout, stderr: stderr}
	go readJSONL(ctx, stdout, stderr, c.Name(), req.Model, events, cmd)
	return llm.NewChannelStream(ctx, events, closer), nil
}

func (c *Client) command(ctx context.Context, req llm.Request, extra ...string) *exec.Cmd {
	args := []string{"-a", "never"}
	args = append(args, "exec", "--json")
	if c.cfg.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	wd := firstNonEmpty(c.cfg.WorkingDirectory, ".")
	args = append(args, "-C", wd)
	if c.cfg.Sandbox != "" {
		args = append(args, "--sandbox", c.cfg.Sandbox)
	}
	args = append(args, c.cfg.ExtraArgs...)
	args = append(args, extra...)
	args = append(args, renderPrompt(req))
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	cmd.Dir = wd
	return cmd
}

type processCloser struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	once   sync.Once
}

func (p *processCloser) Close() error {
	var err error
	p.once.Do(func() {
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
		if p.stderr != nil {
			_ = p.stderr.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			err = p.cmd.Process.Kill()
		}
	})
	return err
}

func readJSONL(ctx context.Context, stdout io.Reader, stderr io.Reader, provider string, model string, out chan<- llm.StreamEvent, cmd *exec.Cmd) {
	defer close(out)
	var text strings.Builder
	s := bufio.NewScanner(stdout)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for s.Scan() {
		raw := append([]byte(nil), s.Bytes()...)
		ev := parseCodexEvent(raw, provider)
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
		select {
		case <-ctx.Done():
			return
		case out <- ev:
		}
	}
	if err := s.Err(); err != nil {
		out <- llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, Error: err}
		return
	}
	if err := cmd.Wait(); err != nil {
		b, _ := io.ReadAll(stderr)
		out <- llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, Error: fmt.Errorf("codex exec failed: %w: %s", err, string(b))}
		return
	}
	out <- llm.StreamEvent{Type: llm.StreamEventCompleted, Provider: provider, Response: &llm.Response{Provider: provider, Model: model, Status: "completed", Text: text.String(), Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: text.String()}}}}
}

func parseCodexEvent(raw []byte, provider string) llm.StreamEvent {
	var env struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, Raw: raw, Error: err}
	}
	ev := llm.StreamEvent{Type: llm.StreamEventUnknown, Provider: provider, EventName: env.Type, Raw: raw}
	switch env.Type {
	case "thread.started", "turn.started":
		ev.Type = llm.StreamEventStarted
	case "item.completed":
		if env.Item.Type == "agent_message" {
			ev.Type = llm.StreamEventTextDelta
			ev.Delta = env.Item.Text
		}
	case "turn.completed":
		ev.Type = llm.StreamEventCompleted
	case "error", "turn.failed":
		ev.Type = llm.StreamEventError
		ev.Error = fmt.Errorf("%s", env.Message)
	}
	return ev
}

func renderPrompt(req llm.Request) string {
	var b strings.Builder
	if req.Instructions != "" {
		b.WriteString(req.Instructions)
		b.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		if m.Content != "" {
			b.WriteString(strings.ToUpper(string(m.Role)))
			b.WriteString(": ")
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	for _, item := range req.InputItems {
		if item.Content != "" {
			b.WriteString(item.Content)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	abs, err := filepath.Abs(fallback)
	if err == nil {
		return abs
	}
	return fallback
}
