package gateway

// DefaultFeatureCatalog는 웹 패널/Discord/Slack adapter가 사용할 수 있는 gateway 기능 표면을 알려줘요.
func DefaultFeatureCatalog() []FeatureDTO {
	return []FeatureDTO{
		{Name: "sessions", Status: "implemented", Description: "session 생성, 목록, 상세, fork를 제공해요.", Endpoints: []string{"GET /api/v1/sessions", "POST /api/v1/sessions", "GET /api/v1/sessions/{session_id}", "POST /api/v1/sessions/{session_id}/fork"}},
		{Name: "session_events", Status: "implemented", Description: "session event JSON replay와 SSE replay를 제공해요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/events"}},
		{Name: "todos", Status: "implemented", Description: "agent todo 상태를 외부 status UI에서 읽을 수 있어요.", Endpoints: []string{"GET /api/v1/sessions/{session_id}/todos"}},
		{Name: "background_runs", Status: "implemented", Description: "run을 즉시 접수하고 background 상태 조회와 취소를 제공해요.", Endpoints: []string{"GET /api/v1/runs", "POST /api/v1/runs", "GET /api/v1/runs/{run_id}", "POST /api/v1/runs/{run_id}/cancel"}},
		{Name: "mcp", Status: "implemented", Description: "MCP server manifest를 API와 SQLite에 저장하고 provider 연결 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/mcp/servers", "POST /api/v1/mcp/servers", "GET /api/v1/mcp/servers/{resource_id}", "PUT /api/v1/mcp/servers/{resource_id}", "DELETE /api/v1/mcp/servers/{resource_id}"}},
		{Name: "skills", Status: "implemented", Description: "Skill manifest를 API와 SQLite에 저장하고 provider skill directory/prompt 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/skills", "POST /api/v1/skills", "GET /api/v1/skills/{resource_id}", "PUT /api/v1/skills/{resource_id}", "DELETE /api/v1/skills/{resource_id}"}},
		{Name: "subagents", Status: "implemented", Description: "Subagent manifest를 API와 SQLite에 저장하고 custom agent 설정으로 재사용할 수 있어요.", Endpoints: []string{"GET /api/v1/subagents", "POST /api/v1/subagents", "GET /api/v1/subagents/{resource_id}", "PUT /api/v1/subagents/{resource_id}", "DELETE /api/v1/subagents/{resource_id}"}},
		{Name: "lsp", Status: "planned", Description: "LSP index/query API는 아직 runtime에 직접 연결되지 않았어요."},
	}
}
