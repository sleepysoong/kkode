# 기본 MCP 설계: Serena + Context7이에요

`kkode`는 provider가 MCP를 지원할 때 기본적으로 코드 지능과 최신 문서 검색을 붙일 수 있어야 해요. 그래서 `app.DefaultProviderOptions(root)`에서 Serena와 Context7 MCP manifest를 만들고, `BuildProviderWithOptions`가 저장된 resource manifest와 합쳐서 provider factory에 전달하게 했어요.

## Serena

Serena는 symbol 중심 코드 검색/편집과 LSP 기반 semantic analysis를 제공하는 coding agent toolkit이에요. Serena 문서/인덱스는 Claude Code 예시에서 `uvx --from git+https://github.com/oraios/serena serena start-mcp-server --context ide-assistant --project $(pwd)` 형태의 MCP 실행을 안내해요.

kkode 기본값은 다음과 같아요.

```json
{
  "name": "serena",
  "kind": "stdio",
  "command": "uvx",
  "args": [
    "--from",
    "git+https://github.com/oraios/serena",
    "serena",
    "start-mcp-server",
    "--context",
    "ide-assistant",
    "--project",
    "<workspace-root>"
  ],
  "tools": ["*"],
  "timeout": 30
}
```

`uvx`가 없으면 기본 manifest에서 Serena를 빼요. 배포 환경에서 강제로 쓰고 싶으면 `KKODE_SERENA_COMMAND`를 지정하면 돼요. 별도 args가 필요하면 `KKODE_SERENA_ARGS`를 쉼표 구분으로 지정해요.

## Context7

Context7 문서는 Node.js 이슈를 피하려면 원격 MCP 서버 `https://mcp.context7.com/mcp`를 사용할 수 있고, rate limit이나 인증이 필요하면 `CONTEXT7_API_KEY` header를 붙이는 구성을 안내해요.

kkode 기본값은 다음과 같아요.

```json
{
  "name": "context7",
  "kind": "http",
  "url": "https://mcp.context7.com/mcp",
  "headers": {
    "CONTEXT7_API_KEY": "${CONTEXT7_API_KEY}"
  },
  "tools": ["*"],
  "timeout": 30
}
```

`CONTEXT7_API_KEY`가 없으면 header 없이 붙여요. 다른 endpoint나 self-hosted proxy를 쓰려면 `KKODE_CONTEXT7_URL`을 바꾸면 돼요.

## 운영 토글

- `KKODE_DEFAULT_MCP=off`: Serena/Context7 기본 MCP를 모두 끄고 저장 resource manifest만 사용해요.
- 저장 resource manifest가 같은 이름(`serena`, `context7`)을 쓰면 명시 설정이 기본값을 덮어써요.
- 권한/승인 레이어는 만들지 않아요. provider가 MCP를 사용할 수 있으면 바로 연결해요.

## 참고한 소스

- Serena MCP Index: https://mcpindex.net/en/mcpserver/oraios-serena
- Context7 troubleshooting/config: https://context7.com/docs/resources/troubleshooting
