package openai

import "github.com/sleepysoong/kkode/llm"

func WebSearchTool(options map[string]any) llm.Tool {
	return builtin("web_search_preview", options)
}

func FileSearchTool(vectorStoreIDs []string, maxNumResults int) llm.Tool {
	opts := map[string]any{"vector_store_ids": vectorStoreIDs}
	if maxNumResults > 0 {
		opts["max_num_results"] = maxNumResults
	}
	return builtin("file_search", opts)
}

func ComputerUseTool(options map[string]any) llm.Tool {
	return builtin("computer_use_preview", options)
}
func CodeInterpreterTool(options map[string]any) llm.Tool {
	return builtin("code_interpreter", options)
}
func ImageGenerationTool(options map[string]any) llm.Tool {
	return builtin("image_generation", options)
}
func MCPTool(serverLabel, serverURL string, headers map[string]string) llm.Tool {
	opts := map[string]any{"server_label": serverLabel, "server_url": serverURL}
	if len(headers) > 0 {
		opts["headers"] = headers
	}
	return builtin("mcp", opts)
}

func builtin(kind string, opts map[string]any) llm.Tool {
	if opts == nil {
		opts = map[string]any{}
	}
	return llm.Tool{Kind: llm.ToolBuiltin, Name: kind, ProviderOptions: opts}
}
