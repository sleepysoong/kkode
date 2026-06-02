package gateway

import "sort"

// DefaultFeatureCatalog는 외부 adapter가 사용할 수 있는 gateway 기능 표면을 알려줘요.
func DefaultFeatureCatalog() []FeatureDTO {
	return []FeatureDTO{
		{Name: "operations", Status: "implemented", Description: "배포 health/readiness probe, API bootstrap discovery, OpenAPI 다운로드를 제공해요.", Endpoints: []string{"GET /healthz", "GET /readyz", "GET /api/v1", "GET /api/v1/openapi.yaml"}},
		{Name: "providers", Status: "implemented", Description: "provider 목록, alias, model, auth, 변환 profile discovery와 provider 단독 preflight/test를 제공해요.", Endpoints: []string{"GET /api/v1/providers", "GET /api/v1/providers/{provider}", "POST /api/v1/providers/{provider}/test"}},
		{Name: "models", Status: "implemented", Description: "외부 adapter가 provider별 모델 선택 UI를 만들 수 있게 model catalog를 제공해요.", Endpoints: []string{"GET /api/v1/models"}},
		{Name: "sessions", Status: "implemented", Description: "session 생성, 목록, 상세를 제공해요.", Endpoints: []string{"GET /api/v1/sessions", "POST /api/v1/sessions", "GET /api/v1/sessions/{session_id}"}},
		{Name: "background_runs", Status: "implemented", Description: "run을 즉시 접수하고 background 상태 조회, 취소, live SSE event, transcript, 재시도를 제공해요.", Endpoints: []string{"GET /api/v1/runs", "POST /api/v1/runs", "GET /api/v1/runs/{run_id}", "GET /api/v1/runs/{run_id}/events", "GET /api/v1/runs/{run_id}/transcript", "POST /api/v1/runs/{run_id}/cancel", "POST /api/v1/runs/{run_id}/retry"}},
	}
}

// DefaultProviderCapabilityCatalog는 provider capability map에 쓰는 key의 의미를 한 곳에서 설명해요.
// provider별 capability map은 true 값만 노출해서 작게 유지하고, 빠진 key의 의미는 이 catalog로 해석해요.
func DefaultProviderCapabilityCatalog() []ProviderCapabilityKeyDTO {
	return []ProviderCapabilityKeyDTO{
		{Name: "tools", Description: "표준 function/custom/built-in tool call을 provider 요청에 포함할 수 있어요."},
		{Name: "custom_tools", Description: "자유 형식 custom tool 호출이나 grammar 기반 tool을 다룰 수 있어요."},
		{Name: "reasoning", Description: "thinking/reasoning 설정이나 reasoning item을 보존할 수 있어요."},
		{Name: "reasoning_summaries", Description: "reasoning summary 요청과 응답 summary를 다룰 수 있어요."},
		{Name: "structured_output", Description: "JSON schema 같은 structured output 형식을 요청할 수 있어요."},
		{Name: "streaming", Description: "stream source를 통해 event 단위 응답을 받을 수 있어요."},
		{Name: "tool_choice", Description: "auto/none/required/특정 tool 같은 tool choice를 전달할 수 있어요."},
		{Name: "parallel_tool_calls", Description: "provider가 병렬 tool call을 생성하거나 허용하는 힌트를 이해해요."},
		{Name: "prompt_refs", Description: "provider prompt 저장소 ID/version/variables를 요청에 실을 수 있어요."},
		{Name: "previous_response_id", Description: "Responses API 계열의 previous_response_id 같은 연속 응답 ID를 이어갈 수 있어요."},
		{Name: "mcp", Description: "MCP server/tool 설정을 provider source나 built-in tool로 연결할 수 있어요."},
		{Name: "skills", Description: "Skill directory나 skill prompt context를 provider/session 설정에 반영할 수 있어요."},
		{Name: "custom_agents", Description: "Subagent/custom agent manifest를 provider/session 설정에 반영할 수 있어요."},
		{Name: "a2a", Description: "Agent-to-Agent endpoint나 task 프로토콜을 provider 기능으로 사용할 수 있어요."},
		{Name: "routing", Description: "계정/모델/source routing을 provider gateway 쪽에서 수행할 수 있어요."},
	}
}

// DefaultProviderPipelineCatalog는 모든 provider가 맞춰야 하는 source 확장 흐름을 설명해요.
// 새 API를 붙일 때는 이 단계 중 converter와 caller만 추가하고 agent/runtime/gateway 타입은 그대로 유지해요.
func DefaultProviderPipelineCatalog() []ProviderPipelineStageDTO {
	return []ProviderPipelineStageDTO{
		{Name: "request", Input: "gateway.RunStartRequest 또는 runtime.RunOptions", Output: "llm.Request", Description: "웹 패널, Discord, CLI 요청을 provider-neutral 표준 요청으로 조립해요."},
		{Name: "convert_request", Input: "llm.Request", Output: "llm.ProviderRequest", Description: "RequestConverter가 OpenAI Responses payload, Copilot prompt, Codex exec payload 같은 source별 요청으로 바꿔요."},
		{Name: "api_call", Input: "llm.ProviderRequest", Output: "llm.ProviderResult 또는 llm.EventStream", Description: "ProviderCaller/ProviderStreamCaller가 HTTP API, SDK session, CLI subprocess, in-memory source를 실제 호출해요."},
		{Name: "convert_response", Input: "llm.ProviderResult", Output: "llm.Response", Description: "ResponseConverter가 provider raw 응답을 표준 text, tool call, reasoning item, usage로 되돌려요."},
		{Name: "agent_loop", Input: "llm.Response", Output: "tool result 또는 final response", Description: "agent가 tool call을 즉시 실행하고 tool output을 다음 llm.Request에 누적해요."},
		{Name: "persist_replay", Input: "runtime trace와 run event", Output: "SQLite session/run/event record", Description: "gateway가 같은 결과를 polling, SSE, retry, transcript UI에서 재사용해요."},
	}
}

// APIIndexLinks는 adapter bootstrap에 필요한 대표 endpoint link를 한 곳에서 관리해요.
func APIIndexLinks() map[string]string {
	return map[string]string{
		"health":          "/healthz",
		"ready":           "/readyz",
		"api_index":       "/api/v1",
		"openapi":         "/api/v1/openapi.yaml",
		"providers":       "/api/v1/providers",
		"provider_detail": "/api/v1/providers/{provider}",
		"provider_test":   "/api/v1/providers/{provider}/test",
		"models":          "/api/v1/models",
		"sessions":        "/api/v1/sessions",
		"session_create":  "/api/v1/sessions",
		"session_detail":  "/api/v1/sessions/{session_id}",
		"runs":            "/api/v1/runs",
		"run_start":       "/api/v1/runs",
		"run_detail":      "/api/v1/runs/{run_id}",
		"run_events":      "/api/v1/runs/{run_id}/events",
		"run_transcript":  "/api/v1/runs/{run_id}/transcript",
		"run_retry":       "/api/v1/runs/{run_id}/retry",
		"run_cancel":      "/api/v1/runs/{run_id}/cancel",
	}
}

func APIIndexOperations() []APIIndexOperationDTO {
	links := APIIndexLinks()
	methods := apiIndexLinkMethods()
	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)
	operations := make([]APIIndexOperationDTO, 0, len(names))
	for _, name := range names {
		method := methods[name]
		if method == "" {
			method = "GET"
		}
		operations = append(operations, APIIndexOperationDTO{Name: name, Method: method, Path: links[name]})
	}
	return operations
}

func apiIndexLinkMethods() map[string]string {
	return map[string]string{
		"provider_test":  "POST",
		"session_create": "POST",
		"run_start":      "POST",
		"run_retry":      "POST",
		"run_cancel":     "POST",
	}
}
