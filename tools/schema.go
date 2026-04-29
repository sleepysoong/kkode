package tools

func objectSchemaRequired(properties map[string]any, requiredNames []string) map[string]any {
	required := make([]any, 0, len(requiredNames))
	for _, name := range requiredNames {
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func stringSchema() map[string]any  { return map[string]any{"type": "string"} }
func integerSchema() map[string]any { return map[string]any{"type": "integer"} }
func booleanSchema() map[string]any { return map[string]any{"type": "boolean"} }
func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}
