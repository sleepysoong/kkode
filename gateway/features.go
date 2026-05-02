package gateway

// DefaultFeatureCatalog는 웹 패널/Discord/Slack adapter가 사용할 수 있는 gateway 기능 표면을 알려줘요.
func DefaultFeatureCatalog() []FeatureDTO {
	return []FeatureDTO{
		{Name: "sessions", Status: "implemented", Description: "session 생성, 목록, 상세, fork를 제공해요.", Endpoints: []string{"GET /api/v1/sessions", "POST /api/v1/sessions", "GET /api/v1/sessions/{session_id}", "POST /api/v1/sessions/{session_id}/fork"}},
		{Name: "session_events", Status: "implemented", Description: "session event JSON replay와 SSE replay를 제공해요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/events"}},
		{Name: "todos", Status: "implemented", Description: "agent todo 상태를 외부 status UI에서 읽고 수정할 수 있어요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/todos", "PUT /api/v1/sessions/{session_id}/todos", "POST /api/v1/sessions/{session_id}/todos", "DELETE /api/v1/sessions/{session_id}/todos/{todo_id}"}},
		{Name: "checkpoints", Status: "implemented", Description: "session checkpoint를 저장하고 replay/복구용 snapshot payload를 조회할 수 있어요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/checkpoints", "POST /api/v1/sessions/{session_id}/checkpoints", "GET /api/v1/sessions/{session_id}/checkpoints/{checkpoint_id}"}},
		{Name: "background_runs", Status: "implemented", Description: "run을 즉시 접수하고 background 상태 조회, 취소, live SSE event를 제공해요.", Endpoints: []string{"GET /api/v1/runs", "POST /api/v1/runs", "GET /api/v1/runs/{run_id}", "GET /api/v1/runs/{run_id}/events", "POST /api/v1/runs/{run_id}/cancel", "POST /api/v1/runs/{run_id}/retry"}},
		{Name: "mcp", Status: "implemented", Description: "MCP server manifest를 API와 SQLite에 저장하고 provider 연결 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/mcp/servers", "POST /api/v1/mcp/servers", "GET /api/v1/mcp/servers/{resource_id}", "PUT /api/v1/mcp/servers/{resource_id}", "DELETE /api/v1/mcp/servers/{resource_id}", "GET /api/v1/mcp/servers/{resource_id}/tools", "POST /api/v1/mcp/servers/{resource_id}/tools/{tool_name}/call"}},
		{Name: "skills", Status: "implemented", Description: "Skill manifest를 API와 SQLite에 저장하고 provider skill directory/prompt 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/skills", "POST /api/v1/skills", "GET /api/v1/skills/{resource_id}", "PUT /api/v1/skills/{resource_id}", "DELETE /api/v1/skills/{resource_id}", "GET /api/v1/skills/{resource_id}/preview"}},
		{Name: "subagents", Status: "implemented", Description: "Subagent manifest를 API와 SQLite에 저장하고 custom agent 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/subagents", "POST /api/v1/subagents", "GET /api/v1/subagents/{resource_id}", "PUT /api/v1/subagents/{resource_id}", "DELETE /api/v1/subagents/{resource_id}", "GET /api/v1/subagents/{resource_id}/preview"}},
		{Name: "lsp", Status: "implemented", Description: "Go source symbol index를 LSP-style API로 조회할 수 있어요.", Endpoints: []string{"GET /api/v1/lsp/symbols", "GET /api/v1/lsp/document-symbols"}},
		{Name: "tools", Status: "implemented", Description: "file/shell/web 표준 tool 목록과 직접 실행 API를 제공해요. 권한 프롬프트 없이 바로 실행해요.", Endpoints: []string{"GET /api/v1/tools", "POST /api/v1/tools/call"}},
		{Name: "files", Status: "implemented", Description: "웹 패널용 파일 목록, 읽기, 쓰기 API를 제공해요. 권한 프롬프트 없이 바로 실행해요.", Endpoints: []string{"GET /api/v1/files", "GET /api/v1/files/content", "PUT /api/v1/files/content"}},
	}
}
