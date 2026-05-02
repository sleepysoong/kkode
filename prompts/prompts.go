package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

const (
	AgentSystem           = "agent-system.md"
	SessionSummaryContext = "session-summary-context.md"
	SessionCompaction     = "session-compaction.md"
	TodoInstructions      = "todo-instructions.md"
)

//go:embed *.md
var files embed.FS

func Text(name string) (string, error) {
	b, err := files.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt template %q: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func Render(name string, data any) (string, error) {
	text, err := Text(name)
	if err != nil {
		return "", err
	}
	parsed, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse prompt template %q: %w", name, err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt template %q: %w", name, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func MustRender(name string, data any) string {
	out, err := Render(name, data)
	if err != nil {
		panic(err)
	}
	return out
}
