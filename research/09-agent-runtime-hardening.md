# 09. 실제 agent runtime 강화 조사 및 구현 메모

작성일: 2026-04-27
목표: `kkode`를 단순 provider wrapper가 아니라 실제 coding agent로 실행할 수 있게 강화해요.

## 조사한 공식/주요 소스

- OpenAI Function calling guide: https://platform.openai.com/docs/guides/function-calling?api-mode=responses
- OpenAI Responses API reference: https://platform.openai.com/docs/api-reference/responses/compact?api-mode=responses
- OpenAI Tools guide: https://platform.openai.com/docs/guides/tools?api-mode=responses
- OpenAI Reasoning models guide: https://platform.openai.com/docs/guides/reasoning?lang=python
- OpenAI Agents SDK guardrails: https://openai.github.io/openai-agents-python/guardrails/
- OpenAI Agents SDK tracing reference: https://openai.github.io/openai-agents-python/ref/tracing/
- GitHub Copilot SDK repository: https://github.com/github/copilot-sdk
- GitHub Copilot SDK custom agents docs: https://docs.github.com/en/copilot/how-tos/copilot-sdk/use-copilot-sdk/custom-agents
- GitHub Copilot SDK getting-started/tools docs: https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md

## 핵심 결론

실제 agent로 쓰려면 provider 호출만 있으면 부족해요. 최소한 다음 구조가 있어야해요.

1. **Model/provider abstraction**이 있어야해요.
2. **Tool definition + local handler registry**가 있어야해요.
3. **Tool loop**가 reasoning item과 function call output을 보존해야해요.
4. **Workspace boundary**가 root 밖 탈출, 쓰기, shell 실행을 제한해야해요.
5. **Guardrail**이 agent 설정과 함께 있어야해요.
6. **Trace/transcript**가 tool 호출, 실패, 최종 응답을 남겨야해요.
7. **CLI entrypoint**가 있어야 앱 밖에서도 바로 smoke test할 수 있어요.

## OpenAI 조사 반영

OpenAI Function calling guide는 tool calling이 “model에 tools를 주고, tool call을 받고, 앱 코드가 실행하고, tool output을 다시 보내고, 최종 응답을 받는” 다단계 흐름이라고 설명해요. `kkode`의 `llm.RunToolLoop`는 이 흐름을 그대로 구현해요.

OpenAI reasoning guide는 reasoning model에서 tool call을 다룰 때 이전 response의 reasoning item과 tool call item을 다음 input에 보존하는 것이 중요하다고 설명해요. 그래서 `RunToolLoop`는 provider가 반환한 `Response.Output`을 다음 `Request.InputItems`에 그대로 붙여요.

OpenAI Responses API reference는 `previous_response_id`, `parallel_tool_calls`, built-in tools, MCP tools, function tools를 같은 response primitive 안에서 다룬다고 설명해요. 그래서 `llm.Request`에는 `PreviousResponseID`, `ParallelToolCalls`, `Include`, `Tools`, `ToolChoice`를 유지해요.

OpenAI Agents SDK guardrails 문서는 guardrail이 agent와 함께 배치되는 구조가 읽기 좋고, 입력 guardrail은 첫 agent 입력에, 출력 guardrail은 최종 출력에 적용된다고 설명해요. 그래서 `agent.Config`에 `Guardrails`를 넣고 `Run` 시작과 최종 응답 뒤에 검사해요.

OpenAI Agents SDK tracing reference는 response/function/guardrail/handoff/MCP span 같은 event shape를 분리해서 추적해요. `kkode`는 아직 OpenTelemetry span까지는 아니지만 `TraceEvent`로 `agent.started`, `tool.started`, `tool.completed`, `tool.failed`, `agent.completed`, `guardrail.*`를 남겨요.

## GitHub Copilot SDK 조사 반영

GitHub Copilot SDK repository는 SDK가 Copilot CLI 뒤의 agent runtime을 프로그램에서 호출하게 해주며 planning, tool invocation, file edits 등을 runtime이 처리한다고 설명해요. 그래서 `providers/copilot`은 OpenAI-compatible HTTP provider가 아니라 session/runtime adapter로 유지해야해요.

Copilot custom agents docs는 custom agent가 system prompt, tool restrictions, MCP servers를 가진 가벼운 정의이며 runtime이 sub-agent로 위임할 수 있다고 설명해요. `kkode`의 장기 구조에서는 `agent.Config`를 Copilot custom agent definition과 OpenAI agent config로 변환 가능한 중립 설정으로 키워야해요.

Copilot SDK tools docs는 tool이 description, parameter schema, handler로 구성되고 runtime이 필요할 때 handler를 호출한다고 설명해요. `kkode`의 `llm.Tool` + `llm.ToolRegistry` + `copilot.ToCopilotTool` 구조가 이 모델과 잘 맞아요.

## 이번 구현 내용

### `agent` package 추가

```go
type Config struct {
    Name          string
    Provider      llm.Provider
    Model         string
    Instructions  string
    BaseRequest   llm.Request
    Workspace     *workspace.Workspace
    Tools         []llm.Tool
    ToolHandlers  llm.ToolRegistry
    MaxIterations int
    Transcript    *transcript.Transcript
    Observer      Observer
    Guardrails    Guardrails
}

func New(cfg Config) (*Agent, error)
func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error)
func (a *Agent) Stream(ctx context.Context, prompt string) (llm.EventStream, error)
```

역할은 다음과 같아요.

- provider와 model 필수값을 검증해요.
- workspace tool과 custom tool을 합쳐요.
- `BaseRequest`로 reasoning/include/metadata 같은 provider 고급 설정을 전달해요.
- `llm.RunToolLoop`를 실행해요.
- tool trace event를 남겨요.
- transcript를 누적해요.
- 입력/출력 guardrail을 검사해요.

### `workspace` tool 강화

기존 read/list/search에 더해 실제 agent 작업에 필요한 도구를 추가했어요.

| Tool | 역할 |
|---|---|
| `workspace_read_file` | 파일을 읽어요 |
| `workspace_write_file` | 허용된 path에 파일을 써요 |
| `workspace_replace_in_file` | 파일 안의 첫 번째 matching text를 교체해요 |
| `workspace_list` | 디렉터리를 나열해요 |
| `workspace_search` | literal 문자열을 검색해요 |
| `workspace_run_command` | 허용된 command prefix만 실행해요 |

중요한 점은 tool definition이 있어도 handler 단계에서 다시 `ApprovalPolicy`를 검사한다는 점이에요. 모델이 tool argument를 조작해도 `Resolve`, `AllowsWrite`, `AllowsCommand`에서 막아야해요.

### `cmd/kkode-agent` 추가

CLI는 실제 agent smoke test와 앱 integration 시작점이에요.

```bash
go run ./cmd/kkode-agent \
  -provider openai \
  -model gpt-5-mini \
  -root . \
  -write \
  -commands "go test,go vet" \
  -reasoning-effort medium \
  -reasoning-summary auto \
  -transcript .kkode/transcript.json \
  "테스트를 실행하고 실패하면 고쳐줘"
```

기본은 read-only예요. 파일 수정은 `-write`, shell 실행은 `-commands`를 줘야 열려요.

## 남은 설계 TODO

- [x] `TraceEvent`를 OpenTelemetry span으로 내보내는 exporter를 추가해야해요.
- [x] `workspace_apply_patch` tool을 별도로 추가해서 큰 파일 전체 쓰기보다 안전한 patch 흐름을 제공해야해요.
- [x] command 실행 결과에 exit code, stderr, elapsed time을 구조화해서 남겨야해요.
- [x] output guardrail을 substring에서 schema/policy 함수 기반으로 확장해야해요.
- [x] Copilot custom agent definition과 `agent.Config` 사이 변환 helper를 추가해야해요.
- [x] OpenAI hosted MCP tool과 local MCP client tool을 같은 설정 파일에서 붙일 수 있게 해야해요.
