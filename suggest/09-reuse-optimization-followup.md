# 재사용/최적화 후속 제안이에요

여러 서브에이전트가 `gateway`, `session/agent/runtime/tools`, `providers/llm/prompts`를 나눠 읽고 확인한 후보를 실행 우선순위로 정리했어요. 이번 패스에서는 바로 효과가 큰 `tools.StandardTools` 공통 surface와 MCP probe 확장을 먼저 반영했어요.

## 이번에 반영한 것

- `tools.StandardTools(SurfaceOptions)`를 추가해서 CLI, gateway, app 조립이 같은 file/shell/web tool 묶음을 쓰게 했어요.
- gateway의 `standardToolSurface` 중복 조립 함수를 제거하고 `tools.StandardTools`를 재사용하게 했어요.
- MCP stdio probe API를 `tools/list`뿐 아니라 `resources/list`, `prompts/list`까지 확장했어요.
- README/ARCHITECTURE/OpenAPI/feature catalog/test를 같이 갱신해서 외부 adapter가 새 API를 발견할 수 있게 했어요.

## 다음 우선순위 후보

### 1. Gateway route spec 단일화가 필요해요

현재 `gateway/server.go`, `gateway/resources.go`, `gateway/todos.go`에 `len(parts)`와 HTTP method 분기가 반복돼요. 다음 단계에서는 내부 `routeSpec` 테이블을 만들고 다음 정보를 한 곳에 모아야 해요.

- method
- path pattern
- feature name
- handler
- not found / method not allowed 메시지

이렇게 하면 `/api/v1/capabilities`, `gateway/openapi_contract_test.go`, 문서 endpoint 목록을 같은 소스에서 검증하기 쉬워져요.

### 2. LSP Go 파일 scan helper가 필요해요

`scanGoSymbols`, `scanGoDefinitions`, `scanGoReferences`, `scanGoDiagnostics`, `scanGoHover`가 `filepath.WalkDir`, skip directory, `.go` 필터, `parser.ParseFile`, limit 처리를 반복해요. `walkGoFiles(root, limit, mode, visitor)` 같은 helper를 만들면 LSP API 성능 튜닝과 skip rule 변경을 한 곳에서 처리할 수 있어요.

### 3. SQLite session 저장 경로를 append 중심으로 바꿔야 해요

`SQLiteStore.SaveSession`은 긴 session에서 turns/events를 모두 지우고 다시 넣기 때문에 write amplification이 커질 수 있어요. 이미 `AppendEvent`, run event append가 있으므로 runtime은 새 turn/event만 append하는 경로를 우선 사용하고, full save는 복구/마이그레이션용으로 남기는 방향이 좋아요.

### 4. Todo 저장을 전용 store 경계로 분리해야 해요

`session.TodoTools`는 현재 `LoadSession -> SaveSession`으로 todo를 저장해요. 병렬 tool call 환경에서는 lost update 위험이 있으므로 `TodoStore` 또는 `SaveTodos`를 통해 todo만 transaction으로 갱신해야 해요.

### 5. OpenAI-compatible request builder 공통화는 반영했어요

`providers/openai.Generate`와 `Stream`은 이제 같은 request builder와 retry 경로를 공유해요. `openai.Config.ProviderName`도 추가해서 OmniRoute 같은 파생 provider가 response와 stream event label을 자기 이름으로 고정할 수 있어요. 다음 단계에서는 OpenAI-compatible chat/embeddings/images까지 surface를 넓힐지 결정하면 돼요.

### 6. Prompt 렌더링 cache를 `llm.Template`까지 넓히면 좋아요

`prompts.Render`는 embed template cache를 쓰지만 `llm.Template.Render`는 매번 parse해요. 동적 provider prompt와 저장형 prompt template을 같은 compiled template primitive로 처리하면 비용과 에러 포맷이 줄어들어요.

## 검증해야 하는 방향이에요

- route 추가 시 `DefaultFeatureCatalog` endpoint가 OpenAPI path/method에 반드시 존재해야 해요.
- 권한/승인/allow/deny/read-only 개념은 다시 만들지 않아야 해요.
- external adapter용 API는 JSON shape, OpenAPI schema, README 예제가 함께 갱신돼야 해요.
