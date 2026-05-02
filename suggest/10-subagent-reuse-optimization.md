# Sub agent 기반 재사용/최적화 개선안이에요

여러 sub agent가 `gateway`, `session/sqlite`, `llm/tools/runtime/agent`를 읽고 찾은 후보를 실제 작업 우선순위로 정리해요. 이번 pass에서는 위험이 낮고 바로 검증 가능한 항목부터 반영했어요.

## 이번에 반영한 항목이에요

1. `GET /api/v1` discovery index를 추가했어요.
   - 외부 Discord/Slack/web adapter가 OpenAPI, capability, session, run, tool, file, git, MCP, skill, subagent, LSP, prompt endpoint를 root index에서 발견할 수 있어요.
   - `APIIndexLinks()`로 대표 link를 한 곳에서 관리해서 handler와 문서가 같은 bootstrap map을 재사용해요.

2. `llm.ToolMiddleware`와 `ToolRegistry.WithMiddleware`를 추가했어요.
   - agent trace wrapping을 registry middleware로 바꿔서 tracing, timeout, metric, redaction 같은 공통 관심사를 tool 실행 표면마다 재사용할 수 있게 했어요.
   - 기존 `ToolRegistry.Execute` 계약은 유지해서 provider tool loop와 gateway 직접 tool call의 결과 형식이 바뀌지 않아요.

## 다음 우선순위 후보예요

1. SQLite append 순번 경합을 줄였어요.
   - `turns(session_id, ordinal)`, `events(session_id, ordinal)`에 unique index를 추가하고, `run_events(run_id, seq)`의 기존 unique 제약과 함께 `retrySQLiteSequence`로 짧게 재시도해요.
   - `MAX(seq)+1`, `MAX(ordinal)+1` 경로가 동시에 같은 값을 잡아도 constraint가 막고 append helper가 다시 계산해요.

2. run 상태 저장과 durable run event 저장을 한 transaction으로 묶었어요.
   - `session.RunSnapshotStore.SaveRunWithEvent`를 추가하고 SQLite 구현체에서 run snapshot과 `run_events` insert를 같은 transaction으로 처리해요.
   - `gateway.AsyncRunManager.persist`는 이 interface가 있으면 SaveRun+AppendRunEvent 분리 경로 대신 원자 저장을 우선 사용해요.

3. Resource 계열 handler의 `LoadResource`/not found/store missing 반복을 helper로 묶었어요.
   - `gateway.Server.withResource`가 MCP/skill/subagent preview와 단건 조회의 store missing, not found 응답을 같은 방식으로 처리해요.

4. LSP도 files/git과 같은 project root 검증 helper를 쓰게 해야 해요.
   - path 검증과 오류 응답이 gateway 전반에서 더 일관돼요.

5. OpenAI-compatible provider transport를 더 공통화해야 해요.
   - OpenAI, OmniRoute, 앞으로 붙을 compatible gateway가 request build/parse/retry/stream 코드를 더 많이 공유할 수 있어요.
