package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"
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

var templateCache sync.Map

func List() ([]string, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func Text(name string) (string, error) {
	b, err := files.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt template %q: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func Render(name string, data any) (string, error) {
	parsed, err := parsedTemplate(name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt template %q: %w", name, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func parsedTemplate(name string) (*template.Template, error) {
	if cached, ok := templateCache.Load(name); ok {
		return cached.(*template.Template), nil
	}
	text, err := Text(name)
	if err != nil {
		return nil, err
	}
	parsed, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse prompt template %q: %w", name, err)
	}
	actual, _ := templateCache.LoadOrStore(name, parsed)
	return actual.(*template.Template), nil
}

func MustRender(name string, data any) string {
	out, err := Render(name, data)
	if err != nil {
		panic(err)
	}
	return out
}
