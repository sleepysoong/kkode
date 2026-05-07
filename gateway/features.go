package gateway

// DefaultFeatureCatalog는 웹 패널/Discord/Slack adapter가 사용할 수 있는 gateway 기능 표면을 알려줘요.
func DefaultFeatureCatalog() []FeatureDTO {
	return []FeatureDTO{
		{Name: "operations", Status: "implemented", Description: "배포 health/readiness probe와 API bootstrap discovery를 제공해요.", Endpoints: []string{"GET /healthz", "GET /readyz", "GET /api/v1"}},
		{Name: "openapi", Status: "implemented", Description: "외부 adapter와 SDK generator가 현재 gateway OpenAPI 계약을 직접 내려받을 수 있어요.", Endpoints: []string{"GET /api/v1", "GET /api/v1/openapi.yaml"}},
		{Name: "sessions", Status: "implemented", Description: "session 생성, 목록, 상세, export/import, turn/transcript 조회, compact, fork를 제공해요.", Endpoints: []string{"GET /api/v1/sessions", "POST /api/v1/sessions", "POST /api/v1/sessions/import", "GET /api/v1/sessions/{session_id}", "GET /api/v1/sessions/{session_id}/export", "GET /api/v1/sessions/{session_id}/turns", "GET /api/v1/sessions/{session_id}/turns/{turn_id}", "GET /api/v1/sessions/{session_id}/transcript", "POST /api/v1/sessions/{session_id}/compact", "POST /api/v1/sessions/{session_id}/fork"}},
		{Name: "session_events", Status: "implemented", Description: "session event JSON replay와 SSE replay를 제공해요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/events"}},
		{Name: "todos", Status: "implemented", Description: "agent todo 상태를 외부 status UI에서 읽고 수정할 수 있어요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/todos", "PUT /api/v1/sessions/{session_id}/todos", "POST /api/v1/sessions/{session_id}/todos", "DELETE /api/v1/sessions/{session_id}/todos/{todo_id}"}},
		{Name: "checkpoints", Status: "implemented", Description: "session checkpoint를 저장하고 replay/복구용 snapshot payload를 조회할 수 있어요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/checkpoints", "POST /api/v1/sessions/{session_id}/checkpoints", "GET /api/v1/sessions/{session_id}/checkpoints/{checkpoint_id}"}},
		{Name: "background_runs", Status: "implemented", Description: "run을 즉시 접수하고 background 상태 조회, 취소, live SSE event, 실행 전 preview, run/request 단위 transcript를 제공해요.", Endpoints: []string{"GET /api/v1/runs", "POST /api/v1/runs", "POST /api/v1/runs/preview", "POST /api/v1/runs/validate", "GET /api/v1/runs/{run_id}", "GET /api/v1/runs/{run_id}/events", "GET /api/v1/runs/{run_id}/transcript", "POST /api/v1/runs/{run_id}/cancel", "POST /api/v1/runs/{run_id}/retry", "GET /api/v1/requests/{request_id}/runs", "GET /api/v1/requests/{request_id}/events", "GET /api/v1/requests/{request_id}/transcript"}},
		{Name: "providers", Status: "implemented", Description: "provider 목록, alias, model, auth, 변환 profile을 discovery해요.", Endpoints: []string{"GET /api/v1/providers", "GET /api/v1/providers/{provider}"}},
		{Name: "models", Status: "implemented", Description: "외부 adapter가 provider별 모델 선택 UI를 만들 수 있게 model catalog를 제공해요.", Endpoints: []string{"GET /api/v1/models"}},
		{Name: "stats", Status: "implemented", Description: "dashboard adapter가 session/run/resource 상태 카운트를 한 번에 볼 수 있어요.", Endpoints: []string{"GET /api/v1/stats"}},
		{Name: "diagnostics", Status: "implemented", Description: "배포 상태, store ping, run starter/previewer 연결, provider/default MCP 개수를 한 번에 점검해요.", Endpoints: []string{"GET /api/v1/diagnostics"}},
		{Name: "prompts", Status: "implemented", Description: "system/session/todo prompt 템플릿 목록, 원문, 렌더링 preview를 제공해요.", Endpoints: []string{"GET /api/v1/prompts", "GET /api/v1/prompts/{template_name}", "POST /api/v1/prompts/{template_name}/render"}},
		{Name: "mcp", Status: "implemented", Description: "MCP server manifest를 API와 SQLite에 저장하고 provider 연결 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/mcp/servers", "POST /api/v1/mcp/servers", "GET /api/v1/mcp/servers/{resource_id}", "PUT /api/v1/mcp/servers/{resource_id}", "DELETE /api/v1/mcp/servers/{resource_id}", "GET /api/v1/mcp/servers/{resource_id}/tools", "GET /api/v1/mcp/servers/{resource_id}/resources", "GET /api/v1/mcp/servers/{resource_id}/resources/read", "GET /api/v1/mcp/servers/{resource_id}/prompts", "POST /api/v1/mcp/servers/{resource_id}/prompts/{prompt_name}/get", "POST /api/v1/mcp/servers/{resource_id}/tools/{tool_name}/call"}},
		{Name: "skills", Status: "implemented", Description: "Skill manifest를 API와 SQLite에 저장하고 provider skill directory/prompt 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/skills", "POST /api/v1/skills", "GET /api/v1/skills/{resource_id}", "PUT /api/v1/skills/{resource_id}", "DELETE /api/v1/skills/{resource_id}", "GET /api/v1/skills/{resource_id}/preview"}},
		{Name: "subagents", Status: "implemented", Description: "Subagent manifest를 API와 SQLite에 저장하고 custom agent 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/subagents", "POST /api/v1/subagents", "GET /api/v1/subagents/{resource_id}", "PUT /api/v1/subagents/{resource_id}", "DELETE /api/v1/subagents/{resource_id}", "GET /api/v1/subagents/{resource_id}/preview"}},
		{Name: "lsp", Status: "implemented", Description: "Go source symbol index, definition/reference, diagnostics, hover를 LSP-style API로 조회할 수 있어요.", Endpoints: []string{"GET /api/v1/lsp/symbols", "GET /api/v1/lsp/document-symbols", "GET /api/v1/lsp/definitions", "GET /api/v1/lsp/references", "GET /api/v1/lsp/diagnostics", "GET /api/v1/lsp/hover"}},
		{Name: "tools", Status: "implemented", Description: "file/shell/web 표준 tool 목록과 직접 실행 API를 제공해요. 권한 프롬프트 없이 바로 실행해요.", Endpoints: []string{"GET /api/v1/tools", "POST /api/v1/tools/call"}},
		{Name: "files", Status: "implemented", Description: "웹 패널용 파일 목록, 읽기, 쓰기, patch, glob, grep API를 제공해요. 권한 프롬프트 없이 바로 실행해요.", Endpoints: []string{"GET /api/v1/files", "GET /api/v1/files/content", "PUT /api/v1/files/content", "POST /api/v1/files/patch", "GET /api/v1/files/glob", "GET /api/v1/files/grep"}},
		{Name: "git", Status: "implemented", Description: "웹 패널이 변경사항을 렌더링할 수 있게 git status, diff, log를 제공해요.", Endpoints: []string{"GET /api/v1/git/status", "GET /api/v1/git/diff", "GET /api/v1/git/log"}},
	}
}

// APIIndexLinks는 adapter bootstrap에 필요한 대표 endpoint link를 한 곳에서 관리해요.
func APIIndexLinks() map[string]string {
	return map[string]string{
		"health":             "/healthz",
		"ready":              "/readyz",
		"openapi":            "/api/v1/openapi.yaml",
		"version":            "/api/v1/version",
		"capabilities":       "/api/v1/capabilities",
		"diagnostics":        "/api/v1/diagnostics",
		"providers":          "/api/v1/providers",
		"provider_detail":    "/api/v1/providers/{provider}",
		"models":             "/api/v1/models",
		"stats":              "/api/v1/stats",
		"sessions":           "/api/v1/sessions",
		"session_import":     "/api/v1/sessions/import",
		"session_export":     "/api/v1/sessions/{session_id}/export",
		"session_events":     "/api/v1/sessions/{session_id}/events",
		"session_transcript": "/api/v1/sessions/{session_id}/transcript",
		"runs":               "/api/v1/runs",
		"run_events":         "/api/v1/runs/{run_id}/events",
		"run_preview":        "/api/v1/runs/preview",
		"run_validate":       "/api/v1/runs/validate",
		"run_transcript":     "/api/v1/runs/{run_id}/transcript",
		"request_runs":       "/api/v1/requests/{request_id}/runs",
		"request_events":     "/api/v1/requests/{request_id}/events",
		"request_transcript": "/api/v1/requests/{request_id}/transcript",
		"tools":              "/api/v1/tools",
		"files":              "/api/v1/files",
		"git":                "/api/v1/git/status",
		"mcp_servers":        "/api/v1/mcp/servers",
		"skills":             "/api/v1/skills",
		"subagents":          "/api/v1/subagents",
		"lsp_symbols":        "/api/v1/lsp/symbols",
		"prompts":            "/api/v1/prompts",
	}
}
