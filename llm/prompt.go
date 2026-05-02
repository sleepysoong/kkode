package llm

import (
	"strings"
)

func UserText(text string) Message      { return Message{Role: RoleUser, Content: text} }
func DeveloperText(text string) Message { return Message{Role: RoleDeveloper, Content: text} }
func SystemText(text string) Message    { return Message{Role: RoleSystem, Content: text} }

func LastUserPrompt(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			if messages[i].Content != "" {
				return messages[i].Content
			}
			parts := make([]string, 0, len(messages[i].Parts))
			for _, part := range messages[i].Parts {
				if part.Text != "" {
					parts = append(parts, part.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

type TranscriptPromptOptions struct {
	InstructionHeader string
}

func RenderTranscriptPrompt(req Request, opts TranscriptPromptOptions) string {
	var b strings.Builder
	if req.Instructions != "" {
		if opts.InstructionHeader != "" {
			b.WriteString(opts.InstructionHeader)
			b.WriteString("\n")
		}
		b.WriteString(req.Instructions)
		b.WriteString("\n\n")
	}
	for _, msg := range req.Messages {
		if msg.Content == "" {
			continue
		}
		b.WriteString(strings.ToUpper(string(msg.Role)))
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	for _, item := range req.InputItems {
		if item.Content != "" {
			b.WriteString(item.Content)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}
