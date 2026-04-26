package llm

import (
	"bytes"
	"text/template"
)

type Template struct {
	Name string
	Text string
}

func (t Template) Render(vars map[string]any) (string, error) {
	parsed, err := template.New(t.Name).Option("missingkey=error").Parse(t.Text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
