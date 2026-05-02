# 08. 최적화와 재사용성 강화 로드맵

이 문서는 여러 read-only subagent 점검 결과를 합쳐서, `kkode`를 프로덕션형 API-only 바이브 코딩 백엔드로 더 다듬기 위한 재사용/최적화 후보를 정리해요. 권한/승인 기능은 의도적으로 제외하고, 현재 정책처럼 YOLO 실행을 유지해야해요.

## 이번 패스에서 바로 반영한 것

1. Gateway limit clamp를 `queryLimit` helper로 통합했어요.
   - 대상: sessions, runs, run events, checkpoints, resources, LSP symbol API예요.
   - 효과: API별 limit 상한 정책을 반복 구현하지 않고 같은 형태로 유지해요.

2. SSE frame writer를 `writeSSEFrame`으로 재사용하게 했어요.
   - 대상: session event SSE, run event SSE예요.
   - 효과: `id`, `event`, `data` framing과 flush 동작이 한 경로를 타요.

3. Tool JSON schema builder를 exported helper로 열었어요.
   - 대상: `tools.ObjectSchema`, `ObjectSchemaRequired`, `StringSchema`, `IntegerSchema`, `BooleanSchema`, `ArraySchema`예요.
   - 효과: session todo tool이 별도 schema builder를 복붙하지 않고 표준 tool schema builder를 재사용해요.

4. Workspace tree walk를 공통 helper로 묶고 무거운 생성물 디렉터리를 건너뛰게 했어요.
   - 대상: `Glob`, `Grep`, `Search`예요.
   - 기본 skip: `.git`, `.serena`, `.cache`, `.next`, `.turbo`, `node_modules`, `dist`, `build`, `.omx/logs`예요.
   - 효과: agent tool loop에서 `file_grep`, `file_glob`이 대형 repo 생성물 때문에 느려지는 일을 줄여요.

5. 웹 패널용 Files API를 별도 endpoint로 추가했어요.
   - `GET /api/v1/files`
   - `GET /api/v1/files/content`
   - `PUT /api/v1/files/content`
   - 효과: 웹 패널이 generic tool call wrapper 없이 파일 브라우저 UI를 만들 수 있어요.

## 다음 최우선 리팩토링 후보

### 1. Gateway route table을 도입해야해요

현재 `handleAPI`, `handleSessions`, `handleRuns`, `handleFiles`, `handleSessionTodos`, `handleSessionCheckpoints`가 모두 `len(parts)`와 method 분기를 직접 다뤄요. endpoint가 계속 늘면 404/405 응답 정책과 OpenAPI 문서가 어긋나기 쉬워요.

권장 방향은 작고 단순한 route catalog예요.

```go
type APIRoute struct {
    Method  string
    Pattern string
    Feature string
    Handler http.HandlerFunc
}
```

처음부터 완전한 router framework를 넣기보다, 현재 `net/http`를 유지하면서 내부 dispatch helper만 추가하는 편이 좋아요. 새 dependency 없이 리뷰 가능한 diff를 유지할 수 있어요.

### 2. Error envelope mapper를 만들어야해요

현재 handler마다 `writeError(w, r, status, code, message)`를 직접 호출해요. `not_found`, `bad_request`, `upstream_failed`, `store_failed` 같은 의미를 typed error로 표준화하면 API adapter가 재시도/사용자 표시를 더 안정적으로 처리할 수 있어요.

예상 형태예요.

```go
type APIError struct {
    Status  int
    Code    string
    Message string
    Cause   error
}

func writeMappedError(w http.ResponseWriter, r *http.Request, err error)
```

### 3. DTO mapper와 clone 정책을 정리해야해요

`SessionDTO`, `ResourceDTO`, `RunDTO`, `CheckpointDTO` 변환 함수가 여러 파일에 흩어져요. metadata map, raw JSON config, run event payload는 aliasing이 생기면 외부 응답 변경이 내부 상태에 영향을 줄 수 있으니 clone 정책을 명확히 해야해요.

권장 방향은 다음이에요.

- shared DTO는 `gateway/dto.go`에 유지해요.
- endpoint-local DTO는 `*_api.go` 옆에 둬요.
- map/raw JSON clone helper를 한 파일에서 관리해요.

### 4. Session 저장을 증분화해야해요

현재 SQLite `SaveSession`은 session 전체를 저장하기 쉬운 구조지만, 긴 세션과 background run이 많아지면 turns/events/todos를 통째로 갱신하는 비용이 커져요. 이미 `AppendEvent`, `RunStore`, `RunEventStore`가 있으니 다음 단계는 turn/todo/checkpoint 단위 저장 API를 더 명확히 나누는 것이 좋아요.

권장 후보예요.

```go
SaveSessionMeta(ctx, session)
AppendTurn(ctx, sessionID, turn)
SaveTodos(ctx, sessionID, todos)
AppendEvent(ctx, sessionID, event)
```

### 5. Provider HTTP client를 공통화해야해요

OpenAI, OmniRoute, Copilot 계열은 인증 헤더, JSON marshal, retry, error body 처리, streaming parse가 반복돼요. `providers/internal/httpx` 같은 내부 패키지를 두면 새 provider를 추가할 때 중복이 줄어요.

권장 helper예요.

```go
BuildJSONRequest(ctx, method, url string, body any) (*http.Request, error)
DoJSONWithRetry(ctx, client, req, out)
ReadErrorBody(resp)
```

Streaming은 `SSE`와 `JSONL` decoder를 공통 인터페이스로 묶으면 좋아요.

### 6. LSP를 gopls 방식으로 확장해야해요

현재 LSP API는 Go parser 기반 symbol/document-symbol이에요. 웹 패널에서 실제 코딩 도구처럼 쓰려면 다음 endpoint가 필요해요.

- definition
- references
- diagnostics
- hover
- rename preview

처음에는 gopls process pool을 두지 말고, 요청 단위 실행과 timeout부터 시작한 뒤 cache/pool을 붙이는 편이 단순해요.

## 검증 기준

각 리팩토링은 다음을 통과해야해요.

```bash
git diff --check
python3 - <<'PY'
import yaml
from pathlib import Path
yaml.safe_load(Path('gateway/openapi.yaml').read_text())
print('openapi yaml ok')
PY
go test ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go test -race ./gateway ./session ./cmd/kkode-gateway
```

## 주의할 점

- 권한/승인/deny/ask/allow 엔진은 다시 넣지 않아야해요.
- 파일/쉘/web tool은 YOLO 실행을 유지해야해요.
- route table이나 error mapper를 도입하더라도 외부 API response shape와 status code를 조용히 바꾸면 안 돼요.
- OpenAPI, `DefaultFeatureCatalog`, README/ARCHITECTURE, handler test를 함께 갱신해야해요.
