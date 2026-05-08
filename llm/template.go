package llm

import (
	"bytes"
	"sync"
	"text/template"
)

type Template struct {
	Name string
	Text string
}

func (t Template) Render(vars map[string]any) (string, error) {
	parsed, err := t.parsed()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type templateCacheKey struct {
	name string
	text string
}

var parsedTemplateCache sync.Map

func (t Template) parsed() (*template.Template, error) {
	key := templateCacheKey{name: t.Name, text: t.Text}
	if cached, ok := parsedTemplateCache.Load(key); ok {
		return cached.(*template.Template), nil
	}
	parsed, err := template.New(t.Name).Option("missingkey=error").Parse(t.Text)
	if err != nil {
		return nil, err
	}
	actual, _ := parsedTemplateCache.LoadOrStore(key, parsed)
	return actual.(*template.Template), nil
}
