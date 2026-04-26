package llm

import "fmt"

func (r Request) Validate() error {
	if r.Model == "" {
		return fmt.Errorf("model is required")
	}
	if len(r.Messages) == 0 && len(r.InputItems) == 0 && r.Prompt == nil {
		return fmt.Errorf("request must include messages, input items, or prompt ref")
	}
	for _, tool := range r.Tools {
		if tool.Name == "" {
			return fmt.Errorf("tool name is required")
		}
		if tool.Kind == ToolFunction && tool.Parameters == nil {
			return fmt.Errorf("function tool %q requires parameters", tool.Name)
		}
	}
	return nil
}
