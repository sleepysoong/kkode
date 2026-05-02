package tools

func ObjectSchemaRequired(properties map[string]any, requiredNames []string) map[string]any {
	required := make([]any, 0, len(requiredNames))
	for _, name := range requiredNames {
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func ObjectSchema(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	return ObjectSchemaRequired(properties, required)
}

func StringSchema() map[string]any  { return map[string]any{"type": "string"} }
func IntegerSchema() map[string]any { return map[string]any{"type": "integer"} }
func BooleanSchema() map[string]any { return map[string]any{"type": "boolean"} }
func ArraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func objectSchemaRequired(properties map[string]any, requiredNames []string) map[string]any {
	return ObjectSchemaRequired(properties, requiredNames)
}

func stringSchema() map[string]any  { return StringSchema() }
func integerSchema() map[string]any { return IntegerSchema() }
func booleanSchema() map[string]any { return BooleanSchema() }
func arraySchema(items map[string]any) map[string]any {
	return ArraySchema(items)
}
