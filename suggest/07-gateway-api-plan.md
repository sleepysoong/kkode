# 07. Gateway API 설계 계획

작성일: 2026-04-29

## 목표

`kkode`를 로컬 CLI만이 아니라 **agent gateway**로 만들기 위한 API 설계 계획이에요. 나중에 이 API를 기반으로 아래를 붙일 수 있어야해요.

- 웹 패널
- Discord bot
- Slack/Telegram 같은 chat integration
- IDE extension
- GitHub/GitLab webhook automation
- 외부 Go/TypeScript SDK
- 장기 실행 worker/queue

핵심은 “모델 provider gateway”가 아니라 **session-aware coding agent gateway**예요. 즉 provider 호출, file tool, web fetch, session resume, event stream, artifact/diff, cost/usage, 외부 webhook을 하나의 API surface로 묶어야해요.

## 설계 원칙

1. **Session이 1급 리소스여야해요.** 웹 패널과 Discord는 같은 session을 이어볼 수 있어야해요.
2. **실시간 event stream이 필수예요.** tool call, stdout, token delta, error, 완료 이벤트를 SSE/WebSocket으로 받아야해요.
3. **REST는 control plane, stream은 data plane이에요.** 생성/조회/취소는 REST, 실행 중 이벤트는 SSE/WebSocket으로 나눠요.
4. **YOLO 기본값을 API에도 명확히 드러내야해요.** 지금 프로젝트 방향은 YOLO라서 dangerous operation을 숨기면 안 돼요.
5. **외부 integration은 webhook-first로 가야해요.** Discord나 GitHub는 gateway 내부 구현에 묶지 말고 webhook adapter로 얇게 붙여요.
6. **OpenAPI spec을 먼저 작성해야해요.** 웹 패널/SDK/Discord bot이 같은 계약을 쓰게 해야해요.
7. **idempotency와 retry를 설계 초기에 넣어야해요.** Discord interaction retry, webhook 중복, 브라우저 재연결이 반드시 생겨요.
8. **event log는 replay 가능해야해요.** 웹 패널 새로고침 후 과거 이벤트를 다시 가져와야해요.

## 현재 kkode 기반 상태

이미 있는 것:

- `session.SQLiteStore`: session/turn/event/todo/checkpoint 저장소예요.
- `runtime.Runtime`: agent + session store 실행 wrapper예요.
- `agent.Agent`: provider/tool loop 실행 단위예요.
- `tools.FileTools`: `file_read`, `file_write`, `file_delete`, `file_move`, `file_edit`, `file_apply_patch`, `file_glob`, `file_grep`, `shell_run`이에요.
- `tools.WebTools`: `web_fetch`예요.
- `providers/*`: OpenAI, Copilot SDK, Codex CLI, OmniRoute provider adapter예요.
- `cmd/kkode-agent`: 단발 CLI + SQLite session 연결이에요.

아직 남은 것:

- run interrupt는 별도 명령으로 분리되지 않았고 cancel/retry 중심이에요.
- artifact API는 아직 없고 transcript/export/checkpoint로 대체하고 있어요.
- session checkpoint는 payload 저장/조회 중심이고, file mutation checkpoint/restore/list/delete/prune는 전용 files API와 tool로 구현됐어요. 아직 conversation rewind와 undo/redo UX는 없어요.
- Discord/webhook adapter가 없어요.

## 추천 패키지 구조

```text
kkode/
├── gateway/
│   ├── server.go             # HTTP server bootstrap이에요
│   ├── routes.go             # REST route wiring이에요
│   ├── sse.go                # SSE stream 구현이에요
│   ├── websocket.go          # WebSocket은 P1이에요
│   ├── auth.go               # API key/session token auth예요
│   ├── middleware.go         # request id, logging, recover, CORS예요
│   ├── dto.go                # API request/response DTO예요
│   ├── errors.go             # API error envelope이에요
│   ├── openapi.yaml          # API 계약이에요
│   └── handlers/
│       ├── sessions.go
│       ├── runs.go
│       ├── events.go
│       ├── files.go
│       ├── tools.go
│       ├── providers.go
│       ├── webhooks.go
│       └── discord.go        # P1 adapter예요
├── runtime/
├── session/
├── tools/
└── providers/
```

나중에 CLI는 이렇게 가면 좋아요.

```bash
kkode serve --addr 127.0.0.1:41234 --state .kkode/state.db
kkode serve --addr 0.0.0.0:41234 --api-key-env KKODE_API_KEY
kkode gateway openapi > gateway/openapi.yaml
```

## API 버전 전략

처음부터 `/api/v1` prefix를 써요.

```text
GET  /healthz
GET  /readyz
GET  /api/v1/version
...
```

Breaking change는 `/api/v2`로 올려요. OpenAPI spec도 `gateway/openapi.yaml`에 고정해요.

## Auth 전략

### Phase 1: 로컬 API key

초기 웹 패널/Discord 개발에는 bearer token이면 충분해요.

```http
Authorization: Bearer kk_live_...
```

서버 옵션:

```bash
kkode serve --api-key kk_live_...
kkode serve --api-key-env KKODE_API_KEY
kkode serve --no-auth-localhost
```

권장 기본값:

- `127.0.0.1` bind면 `--no-auth-localhost` 허용 가능해요.
- `0.0.0.0` bind면 API key 필수여야해요.
- Discord webhook endpoint는 별도 signing secret을 써야해요.

### Phase 2: Web panel session token

웹 패널에는 짧은 수명 token을 발급해요.

```text
POST /api/v1/auth/tokens
DELETE /api/v1/auth/tokens/{id}
```

### Phase 3: OAuth / multi-user

나중에 multi-user SaaS나 remote server로 가면 OAuth/OIDC를 붙여요. 지금은 과해요.

## 공통 response envelope

성공 응답은 리소스별 JSON을 바로 돌려줘도 되지만, error envelope은 통일해야해요.

```json
{
  "error": {
    "code": "session_not_found",
    "message": "session을 찾을 수 없어요",
    "request_id": "req_...",
    "details": {}
  }
}
```

HTTP status 원칙:

| Status | 의미 |
|---|---|
| `200` | 조회/동기 작업 성공이에요 |
| `201` | session/run 생성 성공이에요 |
| `202` | 비동기 작업 접수예요 |
| `400` | 요청 schema가 틀렸어요 |
| `401` | auth 없음/틀림이에요 |
| `403` | 권한 없음이에요 |
| `404` | 리소스 없음이에요 |
| `409` | 이미 실행 중이거나 상태 충돌이에요 |
| `429` | rate limit이에요 |
| `500` | 서버 오류예요 |

## 핵심 리소스 모델

### Session

```json
{
  "id": "sess_...",
  "project_root": "/repo",
  "provider_name": "openai-compatible",
  "model": "gpt-5-mini",
  "agent_name": "build",
  "mode": "build",
  "summary": "...",
  "turn_count": 12,
  "created_at": "2026-04-29T00:00:00Z",
  "updated_at": "2026-04-29T00:10:00Z",
  "metadata": {}
}
```

### Run

Run은 session 안에서 하나의 prompt 실행이에요.

```json
{
  "id": "run_...",
  "session_id": "sess_...",
  "turn_id": "turn_...",
  "status": "queued|running|completed|failed|cancelled",
  "prompt": "테스트를 고쳐줘",
  "started_at": "...",
  "ended_at": "...",
  "error": null
}
```

현재 `runtime.Runtime.Run`은 동기 실행이라 `Run` entity가 없어요. gateway에서는 `RunManager`를 추가해서 background goroutine으로 실행하고, event stream으로 상태를 알려줘야해요.

### Event

```json
{
  "id": "ev_...",
  "session_id": "sess_...",
  "run_id": "run_...",
  "turn_id": "turn_...",
  "seq": 42,
  "type": "tool.started",
  "tool": "file_read",
  "payload": {},
  "error": "",
  "created_at": "..."
}
```

`seq`가 중요해요. SSE reconnect에서 `after_seq`로 replay해야해요.

### Tool

```json
{
  "name": "file_read",
  "description": "workspace 파일을 읽어요",
  "schema": {},
  "category": "file",
  "enabled": true
}
```

### Artifact

웹 패널에는 tool output 전체를 event payload로만 넣기보다 artifact로 분리하는 게 좋아요.

```json
{
  "id": "art_...",
  "session_id": "sess_...",
  "run_id": "run_...",
  "kind": "stdout|stderr|diff|file|web_fetch|json",
  "title": "go test ./... stdout",
  "content_type": "text/plain",
  "size": 1234,
  "created_at": "..."
}
```

초기에는 artifact table 없이 event payload에 넣고, 큰 payload만 파일/SQLite blob으로 빼도 돼요.

## REST endpoint 초안

### Health / version

```http
GET /healthz
GET /readyz
GET /api/v1/version
```

응답:

```json
{
  "version": "0.1.0",
  "commit": "...",
  "state_db": ".kkode/state.db",
  "providers": ["openai", "codex", "copilot", "omniroute"]
}
```

### Sessions

```http
POST   /api/v1/sessions
GET    /api/v1/sessions
GET    /api/v1/sessions/{session_id}
PATCH  /api/v1/sessions/{session_id}
DELETE /api/v1/sessions/{session_id}
POST   /api/v1/sessions/{session_id}/fork
POST   /api/v1/sessions/{session_id}/compact
GET    /api/v1/sessions/{session_id}/export
POST   /api/v1/sessions/import
```

`POST /sessions` request:

```json
{
  "project_root": "/repo",
  "provider": "codex",
  "model": "gpt-5.3-codex",
  "agent": "build",
  "mode": "build",
  "metadata": {
    "source": "web-panel"
  }
}
```

### Runs

```http
POST   /api/v1/runs
GET    /api/v1/runs/{run_id}
POST   /api/v1/runs/{run_id}/cancel
POST   /api/v1/runs/{run_id}/retry
```

`POST /runs` request:

```json
{
  "session_id": "sess_...",
  "prompt": "테스트 실패를 고쳐줘",
  "provider": "openai",
  "model": "gpt-5-mini",
  "stream": true,
  "yolo": true,
  "metadata": {
    "source": "discord",
    "discord_channel_id": "...",
    "discord_message_id": "..."
  }
}
```

응답은 비동기 handle이에요.

```json
{
  "run_id": "run_...",
  "session_id": "sess_...",
  "status": "queued",
  "events_url": "/api/v1/runs/run_.../events"
}
```

### Events / streaming

```http
GET /api/v1/sessions/{session_id}/events?after_seq=0
GET /api/v1/runs/{run_id}/events?after_seq=0
```

SSE event 예시:

```text
event: tool.started
id: 42
data: {"type":"tool.started","tool":"file_read","payload":{"path":"README.md"}}

```

권장 event type:

```text
run.queued
run.started
message.delta
reasoning.delta
tool.started
tool.output
tool.failed
todo.updated
file.changed
artifact.created
run.completed
run.failed
run.cancelled
```

### Files

웹 패널에서 파일 브라우저를 만들려면 agent tool과 별개로 direct file API가 필요해요.

```http
GET  /api/v1/files?project_root=/repo&path=.
GET  /api/v1/files/content?project_root=/repo&path=README.md
PUT  /api/v1/files/content
POST /api/v1/files/delete
POST /api/v1/files/move
POST /api/v1/files/patch
POST /api/v1/files/restore
GET  /api/v1/files/checkpoints
POST /api/v1/files/checkpoints/prune
GET  /api/v1/files/checkpoints/{checkpoint_id}
DELETE /api/v1/files/checkpoints/{checkpoint_id}
GET  /api/v1/files/glob?project_root=/repo&pattern=**/*.go
GET  /api/v1/files/grep?project_root=/repo&pattern=TODO&path_glob=**/*.go
```

`PUT /api/v1/files/content` request:

```json
{
  "project_root": "/repo",
  "path": "README.md",
  "content": "..."
}
```

YOLO 방향이라 별도 승인 없이 바로 쓰지만, adapter는 필요하면 쓰기 전에 `GET /api/v1/files/content`로 preview를 다시 읽고 충돌을 자체 확인하면 돼요.

### Tools

```http
GET  /api/v1/tools
POST /api/v1/tools/call
```

`POST /tools/call`은 디버그/웹 패널용이에요. 보통은 run 안에서 모델이 tool을 호출하게 해야해요.

```json
{
  "session_id": "sess_...",
  "tool": "web_fetch",
  "arguments": {
    "url": "https://example.com",
    "max_bytes": 65536
  }
}
```

### Providers / models

```http
GET  /api/v1/providers
GET  /api/v1/models
POST /api/v1/providers/{provider}/test
```

응답 예시:

```json
{
  "providers": [
    {
      "name": "codex",
      "capabilities": {"tools": true, "streaming": true},
      "auth_status": "local"
    }
  ]
}
```

### Todos

Discord/web panel에서 진행 상황을 보여주려면 todo API가 있어야해요.

```http
GET /api/v1/sessions/{session_id}/todos
PUT /api/v1/sessions/{session_id}/todos
PATCH /api/v1/sessions/{session_id}/todos/{todo_id}
```

### Artifacts

```http
GET /api/v1/sessions/{session_id}/artifacts
GET /api/v1/artifacts/{artifact_id}
DELETE /api/v1/artifacts/{artifact_id}
```

### Webhooks

```http
POST /api/v1/webhooks/github
POST /api/v1/webhooks/gitlab
POST /api/v1/webhooks/discord/interactions
POST /api/v1/webhooks/custom/{name}
```

Webhook endpoint는 일반 bearer token과 별도로 signature 검증을 해야해요.

## Discord integration 설계

Discord는 gateway의 “client adapter”로 두는 게 좋아요.

### Discord bot 기능

- `/kkode ask prompt:<text>`: 새 run 생성해요.
- `/kkode continue session:<id> prompt:<text>`: 기존 session 이어가요.
- `/kkode status session:<id>`: session/todo/run status를 보여줘요.
- `/kkode cancel run:<id>`: 실행 취소해요.
- `/kkode sessions`: 최근 session 목록을 보여줘요.
- `/kkode link`: Discord channel과 kkode session을 연결해요.

### Discord flow

```text
Discord interaction
 -> POST /api/v1/webhooks/discord/interactions
 -> signature 검증
 -> command parse
 -> POST /api/v1/runs
 -> 즉시 202/deferred response
 -> run events subscribe
 -> Discord follow-up message/edit로 progress 업데이트
```

Discord는 응답 제한 시간이 짧으므로 반드시 deferred response가 필요해요. gateway 내부 run은 비동기로 돌리고, Discord adapter가 event를 요약해서 주기적으로 message edit를 해야해요.

### Discord metadata

Run metadata에 다음을 저장해요.

```json
{
  "source": "discord",
  "discord_guild_id": "...",
  "discord_channel_id": "...",
  "discord_user_id": "...",
  "discord_interaction_id": "...",
  "discord_message_id": "..."
}
```

## Web panel 설계

웹 패널은 gateway API만 사용해야해요. 직접 SQLite를 읽지 않아요.

### 화면 구성

- session list sidebar
- conversation view
- event/tool timeline
- todo panel
- file explorer
- diff/artifact viewer
- provider/model selector
- run/cancel/retry buttons

### 필요한 API

- `GET /sessions`
- `GET /sessions/{id}`
- `POST /runs`
- `GET /runs/{id}/events`
- `GET /api/v1/files`
- `GET /api/v1/files/content`
- `PUT /api/v1/files/content`
- `POST /api/v1/files/delete`
- `POST /api/v1/files/move`
- `POST /api/v1/files/patch`
- `POST /api/v1/files/restore`
- `GET /api/v1/files/checkpoints`
- `POST /api/v1/files/checkpoints/prune`
- `GET /api/v1/files/checkpoints/{checkpoint_id}`
- `DELETE /api/v1/files/checkpoints/{checkpoint_id}`
- `GET /artifacts`
- `GET /todos`

### 이벤트 처리

웹 패널은 SSE를 기본으로 쓰고, WebSocket은 P1로 미뤄도 돼요.

이유:

- SSE는 브라우저 기본 지원이 좋아요.
- server -> client event만 있으면 초기 UI는 충분해요.
- prompt 전송/cancel은 REST로 하면 돼요.

## RunManager 설계

현재 `runtime.Runtime.Run`은 동기 실행이에요. Gateway에는 background 실행 관리자가 필요해요.

```go
type RunManager struct {
    Store      session.Store
    RuntimeFn  func(req RunRequest) (*runtime.Runtime, error)
    Events     EventBus
    Runs       RunStore
}

func (m *RunManager) Start(ctx context.Context, req RunRequest) (*Run, error)
func (m *RunManager) Cancel(ctx context.Context, runID string) error
func (m *RunManager) Get(ctx context.Context, runID string) (*Run, error)
```

RunManager는 context cancellation을 관리해야해요.

```go
type runningRun struct {
    RunID  string
    Cancel context.CancelFunc
    Done   chan struct{}
}
```

## EventBus 설계

```go
type EventBus interface {
    Publish(ctx context.Context, ev GatewayEvent) error
    Subscribe(ctx context.Context, filter EventFilter) (<-chan GatewayEvent, error)
    Replay(ctx context.Context, filter EventFilter) ([]GatewayEvent, error)
}
```

초기 구현:

- SQLite `session.events`를 replay source로 써요.
- 메모리 pub/sub으로 live event를 뿌려요.
- server restart 후 live는 끊기지만 replay는 돼요.

나중에:

- Redis pub/sub
- NATS
- Postgres listen/notify

## DTO와 내부 타입 분리

내부 `session.Session`, `runtime.RunResult`, `llm.Response`를 그대로 외부 API로 노출하면 나중에 변경이 어려워요.

따라서 `gateway/dto.go`에 외부 타입을 별도로 둬요.

```go
type SessionDTO struct { ... }
type RunDTO struct { ... }
type EventDTO struct { ... }
type ToolDTO struct { ... }
```

변환 함수:

```go
func ToSessionDTO(s *session.Session) SessionDTO
func ToEventDTO(ev session.Event) EventDTO
```

## OpenAPI 우선 계획

`gateway/openapi.yaml`을 먼저 쓰고 handler를 맞추는 게 좋아요.

최소 spec 범위:

- sessions
- runs
- events SSE
- files
- tools
- providers/models
- todos
- health/version

생성 후보:

- `oapi-codegen`으로 server interface/client를 생성해요.
- 또는 chi/net/http 수작업 handler + OpenAPI spec 수동 유지로 시작해요.

초기에는 수동 handler가 더 빠르지만, 웹 패널과 Discord bot이 붙기 시작하면 `oapi-codegen`이 좋아요.

## API 구현 단계

### Phase A: Gateway MVP

작업:

- `gateway.Server` 추가해요.
- `GET /healthz`, `GET /api/v1/version` 구현해요.
- `POST /api/v1/sessions`, `GET /api/v1/sessions`, `GET /api/v1/sessions/{id}` 구현해요.
- `POST /api/v1/runs`는 일단 동기 실행 후 결과를 반환해요.
- `GET /api/v1/sessions/{id}/events`는 저장된 event JSON을 반환해요.
- `cmd/kkode serve` 또는 `cmd/kkode-gateway`를 추가해요.

완료 기준:

- curl로 session 생성/실행/조회가 돼요.
- 기존 SQLite state DB를 그대로 써요.
- `go test ./...` 통과해요.

### Phase B: SSE + async run

작업:

- `RunManager` 추가해요.
- `POST /runs`가 즉시 `202`와 `run_id`를 반환해요.
- `GET /runs/{id}/events`가 SSE를 제공해요.
- cancel endpoint를 추가해요.

완료 기준:

- 웹 클라이언트가 run 진행 상황을 실시간으로 받아요.
- reconnect 시 `after_seq` 이후 event를 replay해요.

### Phase C: Web panel support

작업:

- files API 추가해요.
- artifact API 추가해요.
- todo API 추가해요.
- CORS/local auth 설정 추가해요.

완료 기준:

- React/Svelte/HTMX 어떤 패널이든 gateway API만으로 session과 파일을 볼 수 있어요.

### Phase D: Discord adapter

작업:

- Discord interaction signature 검증 구현해요.
- `/kkode ask`, `/kkode continue`, `/kkode status`, `/kkode cancel` 구현해요.
- Discord follow-up message updater를 구현해요.

완료 기준:

- Discord channel에서 prompt를 던지면 gateway run이 생성돼요.
- 진행 상황/todo/final result가 Discord message로 갱신돼요.

### Phase E: Public SDK

작업:

- OpenAPI client 생성 또는 수동 Go SDK 작성해요.
- TypeScript client 생성해요.
- examples 추가해요.

완료 기준:

- 외부 Go 앱에서 `client.Runs.Start()`와 `client.Events.Stream()`을 사용할 수 있어요.
- 웹 패널도 같은 client를 써요.

## 최소 curl 예시

### session 생성

```bash
curl -X POST http://127.0.0.1:41234/api/v1/sessions \
  -H 'Authorization: Bearer kk_live_local' \
  -H 'Content-Type: application/json' \
  -d '{
    "project_root": "/home/user/kkode",
    "provider": "codex",
    "model": "gpt-5.3-codex",
    "agent": "build"
  }'
```

### run 시작

```bash
curl -X POST http://127.0.0.1:41234/api/v1/runs \
  -H 'Authorization: Bearer kk_live_local' \
  -H 'Content-Type: application/json' \
  -d '{
    "session_id": "sess_...",
    "prompt": "README를 개선해줘",
    "yolo": true
  }'
```

### event stream

```bash
curl -N http://127.0.0.1:41234/api/v1/runs/run_.../events \
  -H 'Authorization: Bearer kk_live_local'
```

## 데이터베이스 확장 제안

현재 `session` SQLite schema에 events/turns는 있어요. Gateway에는 run과 artifact table이 추가되면 좋아요.

```sql
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  turn_id TEXT,
  status TEXT NOT NULL,
  prompt TEXT NOT NULL,
  metadata_json BLOB NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  started_at TEXT,
  ended_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE artifacts (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  run_id TEXT,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  content_type TEXT NOT NULL,
  bytes BLOB,
  path TEXT,
  size INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
```

`session.Event`에도 `seq`나 ordinal을 외부 API에서 안정적으로 노출해야해요.

## 보안/운영 주의점

YOLO 모드라 gateway를 외부에 공개하면 위험해요. 특히 `shell_run`, `file_write`, `file_apply_patch`, `web_fetch`가 모두 열려 있어요.

초기 기본 운영 권장:

- 기본 bind는 `127.0.0.1`이에요.
- remote bind는 `--api-key` 없으면 거부해야해요.
- CORS default는 deny예요.
- Discord webhook endpoint만 public tunnel로 열고 gateway 본체는 private로 두는 게 좋아요.
- request body size limit을 둬야해요.
- run concurrency limit을 둬야해요.
- `web_fetch` max bytes와 timeout을 강제해야해요.
- session state DB backup/export를 제공해야해요.

## 먼저 만들 파일 목록

```text
gateway/server.go
gateway/dto.go
gateway/routes.go
gateway/errors.go
gateway/sse.go
gateway/openapi.yaml
cmd/kkode-gateway/main.go
```

초기에는 `chi` 같은 router dependency 없이 `net/http`와 `http.ServeMux`로 시작해도 돼요. route가 늘어나면 `chi`를 검토해요.

## P0 체크리스트

- [x] `gateway.Server`와 `Config`를 만든다.
- [x] health/version endpoint를 만든다.
- [x] session list/create/get endpoint를 만든다.
- [x] run start endpoint를 만든다.
- [x] event list endpoint를 만든다.
- [x] SSE stream endpoint를 만든다.
- [x] API key middleware를 만든다.
- [x] `cmd/kkode-gateway` 또는 `kkode serve`를 만든다.
- [x] `gateway/openapi.yaml`을 작성한다.
- [x] curl smoke script를 만든다.

## 결론

다음 구현은 `gateway.Server` MVP부터 가는 게 좋아요. session store와 runtime은 이미 있으므로, 가장 짧은 path는 아래예요.

1. `gateway` 패키지 추가
2. `POST /sessions`, `GET /sessions`, `GET /sessions/{id}`
3. `POST /runs` 동기 실행
4. `GET /sessions/{id}/events`
5. `cmd/kkode-gateway`
6. 그 다음 SSE/async run으로 확장
