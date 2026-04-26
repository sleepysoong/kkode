package llm

import "strings"

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
