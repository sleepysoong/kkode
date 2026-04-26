package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

type Workspace struct {
	Root           string
	Approval       llm.ApprovalPolicy
	CommandTimeout time.Duration
}

func New(root string, policy llm.ApprovalPolicy) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	return &Workspace{Root: abs, Approval: policy, CommandTimeout: 30 * time.Second}, nil
}

func (w *Workspace) Resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is required")
	}
	var path string
	if filepath.IsAbs(rel) {
		path = filepath.Clean(rel)
	} else {
		path = filepath.Join(w.Root, rel)
	}
	path = filepath.Clean(path)
	root := filepath.Clean(w.Root)
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return path, nil
}

func (w *Workspace) ReadFile(rel string) (string, error) {
	path, err := w.Resolve(rel)
	if err != nil {
		return "", err
	}
	if !w.Approval.AllowsRead(path) {
		return "", fmt.Errorf("read denied: %s", rel)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (w *Workspace) WriteFile(rel, content string) error {
	path, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	if !w.Approval.AllowsWrite(path) {
		return fmt.Errorf("write denied: %s", rel)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (w *Workspace) List(rel string) ([]string, error) {
	path, err := w.Resolve(firstNonEmpty(rel, "."))
	if err != nil {
		return nil, err
	}
	if !w.Approval.AllowsRead(path) {
		return nil, fmt.Errorf("list denied: %s", rel)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out, nil
}

func (w *Workspace) Search(needle string) ([]string, error) {
	if needle == "" {
		return nil, errors.New("needle is required")
	}
	var matches []string
	err := filepath.WalkDir(w.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !w.Approval.AllowsRead(path) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.Contains(b, []byte(needle)) {
			rel, _ := filepath.Rel(w.Root, path)
			matches = append(matches, rel)
		}
		return nil
	})
	return matches, err
}

func (w *Workspace) Run(ctx context.Context, command string, args ...string) (string, error) {
	full := strings.Join(append([]string{command}, args...), " ")
	if !w.Approval.AllowsCommand(full) {
		return "", fmt.Errorf("command denied: %s", full)
	}
	timeout := w.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = w.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func (w *Workspace) Tools() (defs []llm.Tool, handlers llm.ToolRegistry) {
	strict := true
	defs = []llm.Tool{
		{Kind: llm.ToolFunction, Name: "workspace_read_file", Description: "Read a file inside the workspace", Strict: &strict, Parameters: objectSchema(map[string]any{"path": stringSchema()})},
		{Kind: llm.ToolFunction, Name: "workspace_list", Description: "List a directory inside the workspace", Strict: &strict, Parameters: objectSchema(map[string]any{"path": stringSchema()})},
		{Kind: llm.ToolFunction, Name: "workspace_search", Description: "Search for a literal string in workspace files", Strict: &strict, Parameters: objectSchema(map[string]any{"needle": stringSchema()})},
	}
	handlers = llm.ToolRegistry{
		"workspace_read_file": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path string `json:"path"`
		}) (string, error) {
			return w.ReadFile(in.Path)
		}),
		"workspace_list": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path string `json:"path"`
		}) (string, error) {
			xs, err := w.List(in.Path)
			return strings.Join(xs, "\n"), err
		}),
		"workspace_search": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Needle string `json:"needle"`
		}) (string, error) {
			xs, err := w.Search(in.Needle)
			return strings.Join(xs, "\n"), err
		}),
	}
	return defs, handlers
}

func objectSchema(properties map[string]any) map[string]any {
	required := make([]any, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
