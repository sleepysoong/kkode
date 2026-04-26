# 03. ChatGPT 구독을 Codex/OpenCode/OpenClaw에서 API처럼 쓰는 흐름과 Go SDK 후보

작성일: 2026-04-26

## 먼저 결론

사용자가 말한 “ChatGPT 구독한 거를 API처럼 쓸 수 있냐”는 질문은 구분이 필요하다.

### 공식적으로 안전한 표현

- **Codex는 ChatGPT Plus/Pro/Business/Edu/Enterprise 플랜에 포함**된다.
- Codex CLI/IDE/Web/App에서 **Sign in with ChatGPT**로 구독 기반 사용이 가능하다.
- Codex는 CLI, IDE, Web, App, 그리고 SDK/CI/CD surface를 가진다.
- OpenAI Platform API는 별도 API key/조직/과금 체계다.

### 위험한 표현

- “ChatGPT Plus를 일반 OpenAI API key처럼 무제한 API로 쓴다”는 식으로 일반화하면 안 된다.
- OAuth로 Codex backend를 호출하는 비공식 플러그인/프록시는 존재하지만, 이것은 공식 Platform API와 같지 않고 정책/차단/호환성 리스크가 있다.
- 개인 개발 도구에서는 동작하더라도, production/multi-user 앱이면 공식 API key 또는 공식 SDK/계약 경로를 써야 한다.

## 공식 OpenAI/Codex 쪽 확인사항

OpenAI Help Center의 최신 문서 기준:

- Codex는 ChatGPT Plus, Pro, Business, Enterprise/Edu 플랜에 포함된다.
- Codex Free/Go 포함 및 2x rate limit 같은 문구는 “limited time” 조건이 붙어 있으므로 장기 설계의 전제로 삼으면 안 된다.
- 시작 방법은 Codex client를 열고 ChatGPT로 로그인하는 것이다.
- Codex CLI, IDE extension, Codex web 등이 공식 surface다.
- Codex web은 GitHub 연결이 필요하다.
- Codex 사용량은 작업 크기/복잡도/컨텍스트/실행 위치에 따라 다르게 소모된다.

OpenAI developer docs 기준:

- Codex는 OpenAI의 coding agent다.
- IDE, CLI, web/mobile, CI/CD pipelines with SDK 같은 surface가 있다.
- 코드 생성 모델을 직접 앱에 붙이고 싶으면 OpenAI API Responses API에서 Codex/GPT 모델을 호출하는 경로가 별도로 있다.

## OpenAI Codex CLI 공식 흐름

설치:

```bash
npm install -g @openai/codex
# 또는
brew install --cask codex
```

로그인:

```bash
codex
# UI에서 "Sign in with ChatGPT" 선택
```

또는 문서/버전에 따라:

```bash
codex --login
```

공식 저장소 README는 ChatGPT 계정으로 로그인해 Plus/Pro/Business/Edu/Enterprise 플랜의 일부로 Codex를 사용하는 것을 권장하고, API key 사용은 추가 setup이 필요하다고 설명한다.

## OpenClaw 쪽 흐름

조사된 OpenClaw 문서에는 Codex 관련 경로가 3개로 나뉜다.

| 모델 ref | 의미 |
|---|---|
| `openai/gpt-5.4` | OpenAI provider / Platform API key 기반 |
| `openai-codex/gpt-5.4` | OpenAI Codex OAuth provider 경로 |
| `codex/gpt-5.4` | bundled Codex provider + Codex app-server harness |

OpenClaw Codex harness 문서 기준:

- `codex/*` 모델 ref일 때 bundled `codex` plugin과 Codex app-server harness를 사용한다.
- Codex app-server `0.118.0` 이상이 요구된다.
- 인증은 app-server process가 접근 가능한 Codex auth/API key/`~/.codex` 파일 등으로 해결한다.
- OpenClaw는 채널, 세션 파일, 모델 선택, tools, approvals, transcript mirror를 계속 관리하고, low-level agent execution만 Codex harness가 담당한다.

예시 설정:

```jsonc
{
  "plugins": {
    "entries": {
      "codex": {
        "enabled": true
      }
    }
  },
  "agents": {
    "defaults": {
      "model": "codex/gpt-5.4",
      "embeddedHarness": {
        "runtime": "codex",
        "fallback": "none"
      }
    }
  }
}
```

OpenClaw OAuth 문서에는 subscription auth via OAuth가 OpenAI Codex(ChatGPT OAuth) 같은 provider에 쓰인다고 설명되어 있다. 또한 token sink/auth profile 저장소를 따로 두는 이유는 OAuth refresh token 교체 때문에 여러 도구가 서로 로그아웃시키는 문제를 줄이기 위한 것이다.

주의:

- OpenClaw Launch 페이지에는 “ChatGPT Plus를 agent brain으로, API key 없이”라는 마케팅 문구가 있다. 하지만 운영 설계에서는 공식 OpenAI Help/Developer docs와 OpenClaw의 기술문서를 우선해야 한다.
- OpenClaw 문서의 model name은 빠르게 바뀔 수 있다. `/codex models`, `/codex status` 같은 live discovery 명령으로 현재 계정/버전에서 가능한 모델을 확인해야 한다.

## OpenCode 쪽 흐름

조사된 OpenCode 관련 흐름은 크게 3개다.

### 1. OpenCode 자체 Go CLI/agent

`opencode-ai/opencode`는 Go로 빌드되는 터미널 AI coding agent다.

```bash
go install github.com/opencode-ai/opencode@latest
```

저장소 문서상 GitHub Copilot 설정 파일 또는 token을 읽는 provider 경로가 있으며, self-hosted OpenAI-like provider도 사용할 수 있다.

### 2. opencode-sdk-go

`github.com/sst/opencode-sdk-go` / `github.com/anomalyco/opencode-sdk-go`는 Opencode REST API 접근용 Go SDK다.

설치 예:

```bash
go get github.com/sst/opencode-sdk-go@latest
```

기본 사용 예:

```go
package main

import (
    "context"
    "fmt"

    opencode "github.com/sst/opencode-sdk-go"
)

func main() {
    client := opencode.NewClient()

    sessions, err := client.Session.List(context.TODO(), opencode.SessionListParams{})
    if err != nil {
        panic(err)
    }
    fmt.Printf("%+v\n", sessions)
}
```

이 SDK는 “ChatGPT OAuth를 API처럼 쓰는 SDK”라기보다 **Opencode REST API client**다. 즉, Opencode 서버/API를 제어하는 용도에 가깝다.

### 3. OpenCode용 ChatGPT/Codex OAuth 플러그인

`numman-ali/opencode-openai-codex-auth`는 OpenCode에서 ChatGPT Plus/Pro subscription 기반 Codex OAuth 사용을 돕는 비공식 plugin이다.

설치 예:

```bash
npx -y opencode-openai-codex-auth@latest
opencode auth login
opencode run "write hello world to test.txt" --model=openai/gpt-5.2 --variant=medium
```

README 기준 특징:

- ChatGPT Plus/Pro OAuth authentication.
- Codex backend를 OpenCode에 연결.
- 자동 token refresh와 model presets.
- 개인 개발용이며 production/multi-user 앱은 OpenAI Platform API 사용을 권장.

이런 플러그인은 실제로 사용자가 말한 “OpenCode 같은 곳에서 ChatGPT 구독을 API처럼 쓰는” 체감에 가장 가깝다. 그러나 공식 OpenAI Platform API가 아니므로 다음 리스크가 있다.

- backend/API surface가 바뀌면 깨질 수 있음.
- OAuth scope/Cloudflare/ratelimit/정책 변경에 영향받을 수 있음.
- multi-user/상용 서비스에 쓰면 약관/보안/감사 문제가 생길 수 있음.
- token 저장 위치와 refresh token 보호가 매우 중요.

## Go에서 Codex를 다루는 SDK 후보

### 후보 A: 공식 OpenAI SDK + Responses API

“코드 생성 모델을 Go 앱에서 호출”이 목적이면 가장 안정적인 경로는 OpenAI Platform API를 쓰는 것이다.

```bash
go get github.com/openai/openai-go@latest
```

예시:

```go
package main

import (
    "context"
    "fmt"
    "os"

    openai "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    "github.com/openai/openai-go/responses"
)

func main() {
    client := openai.NewClient(
        option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
    )

    // openai-go v1.12.0 기준 컴파일 검증. 최신 버전에서는 go doc으로 재확인하라.
    resp, err := client.Responses.New(context.Background(), responses.ResponseNewParams{
        Model: "gpt-5.4",
        Input: responses.ResponseNewParamsInputUnion{
            OfString: openai.String("Write a small Go HTTP server with tests."),
        },
    })
    if err != nil {
        panic(err)
    }

    fmt.Println(resp.OutputText())
}
```

장점:

- 공식 API 경로.
- production/multi-user에 적합.
- auth/과금/데이터 처리 정책이 명확.

단점:

- ChatGPT 구독 quota가 아니라 OpenAI Platform billing/API key 기반.

### 후보 B: `github.com/picatz/openai/codex`

`pkg.go.dev` 기준 `github.com/picatz/openai/codex`는 “OpenAI codex CLI와 상호작용하는 Go SDK”라고 설명한다.

설치:

```bash
go get github.com/picatz/openai/codex@latest
```

개념 예시:

```go
package main

import (
    "context"
    "fmt"

    codex "github.com/picatz/openai/codex"
)

func main() {
    ctx := context.Background()

    // 실제 생성자/메서드명은 버전별 docs를 확인해야 한다.
    // 이 SDK는 OpenAI 공식 SDK가 아니며 Codex CLI/TS SDK 형태를 Go로 감싼 계열이다.
    client := codex.NewClient()

    session, err := client.NewSession(ctx, codex.SessionOptions{
        Model: "gpt-5.4-codex",
        WorkingDirectory: ".",
    })
    if err != nil {
        panic(err)
    }

    result, err := session.Run(ctx, "Find bugs in this repository and suggest a patch.")
    if err != nil {
        panic(err)
    }

    fmt.Println(result)
}
```

> 위 코드는 사용 패턴 설명용 pseudo-real 예시다. 이 package는 비공식이고 API가 바뀔 수 있으므로 실제 도입 전 `pkg.go.dev/github.com/picatz/openai/codex`와 repository examples를 확인해야 한다.

### 후보 C: Codex CLI를 subprocess로 감싸기

공식 Go SDK가 없거나 preview SDK가 불안정하면 CLI를 subprocess로 호출하는 방법이 가장 단순하다.

```go
package main

import (
    "bytes"
    "context"
    "fmt"
    "os/exec"
    "time"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    cmd := exec.CommandContext(ctx, "codex", "exec", "--json", "explain this repository")
    cmd.Dir = "/path/to/repo"

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    if err := cmd.Run(); err != nil {
        panic(fmt.Sprintf("codex failed: %v\nstderr=%s", err, stderr.String()))
    }

    fmt.Println(stdout.String())
}
```

장점:

- Codex CLI가 지원하는 인증/설정을 그대로 사용.
- Go 앱에서 최소 구현으로 연결 가능.

단점:

- CLI 출력 format 변경에 취약.
- 동시성/세션/streaming 처리가 번거롭다.
- production API가 아니다.

### 후보 D: OpenClaw/OpenCode local API를 Go에서 호출

OpenClaw/OpenCode가 local server 또는 REST API를 열고 있다면 Go에서는 해당 HTTP API를 호출하는 client를 만들 수 있다. 특히 `opencode-sdk-go`가 이 범주에 해당한다.

```go
package main

import (
    "context"
    "fmt"

    opencode "github.com/sst/opencode-sdk-go"
)

func main() {
    client := opencode.NewClient()

    // 실제 create/send params는 opencode-sdk-go api.md와 버전을 확인.
    sessions, err := client.Session.List(context.Background(), opencode.SessionListParams{})
    if err != nil {
        panic(err)
    }
    fmt.Println(sessions)
}
```

## 권장 판단

| 목적 | 추천 경로 |
|---|---|
| Go 앱에서 안정적으로 코드 생성 모델 호출 | OpenAI Platform API + 공식 Go SDK |
| 로컬 개발에서 ChatGPT 구독으로 Codex 사용 | 공식 Codex CLI/IDE/App에서 ChatGPT sign-in |
| OpenClaw 안에서 Codex harness 사용 | OpenClaw `codex/*` + bundled codex plugin + app-server auth |
| OpenCode에서 ChatGPT OAuth 체감 사용 | 비공식 plugin 사용 가능하나 개인용/리스크 감수 |
| Go로 OpenCode API 제어 | `opencode-sdk-go` |
| Go로 Codex CLI 감싸기 | subprocess wrapper 또는 비공식 `picatz/openai/codex` 검토 |

## 보안/운영 주의사항

- ChatGPT OAuth token/refresh token은 API key만큼 민감하게 보호한다.
- `~/.codex/auth.json`, `~/.openclaw/.../auth-profiles.json`, OpenCode auth 파일은 git에 절대 넣지 않는다.
- 여러 도구가 같은 OAuth grant를 refresh하면 서로 로그아웃시키거나 refresh token이 교체될 수 있다.
- 비공식 Codex backend 호출은 갑자기 401/403/429/Cloudflare 차단이 생길 수 있다.
- 회사/팀/고객 데이터 처리에는 공식 API/Enterprise 계약/감사 로그가 있는 경로를 우선한다.

## 소스 검증

- OpenAI Help Center, Using Codex with your ChatGPT plan: https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan/  
  - Codex 포함 플랜, ChatGPT sign-in 시작 방법, 사용량 제한/데이터 처리 확인.
- OpenAI developer docs, Code generation: https://developers.openai.com/api/docs/guides/code-generation  
  - Codex가 IDE/CLI/web/mobile/CI-CD SDK surface를 가진 coding agent라는 점 확인.
- OpenAI Codex CLI repository: https://github.com/openai/codex  
  - 설치, ChatGPT plan sign-in 권장, API key는 별도 setup이라는 점 확인.
- OpenClaw Codex harness docs: https://docs.openclaw.ai/plugins/codex-harness  
  - `openai/*`, `openai-codex/*`, `codex/*` route 차이, app-server 요구사항, config 확인.
- OpenClaw OAuth docs: https://openclawx.cloud/en/concepts/oauth  
  - subscription auth via OAuth, auth profile/token sink 설명 확인.
- OpenCode repository: https://github.com/opencode-ai/opencode  
  - Go 기반 CLI, Copilot config/token, self-hosted OpenAI-like provider 지원 확인.
- OpenCode Go SDK: https://github.com/anomalyco/opencode-sdk-go  
  - Opencode REST API용 Go library, 설치/사용 예 확인.
- opencode-openai-codex-auth: https://github.com/numman-ali/opencode-openai-codex-auth  
  - ChatGPT Plus/Pro OAuth plugin, 개인 개발용/production에는 Platform API 권장 확인.
- `github.com/picatz/openai/codex` pkg.go.dev: https://pkg.go.dev/github.com/picatz/openai/codex  
  - OpenAI codex CLI와 상호작용하는 비공식 Go SDK 설명 확인.


## 로컬 Go 컴파일 검증 메모

2026-04-26에 Go `go1.26.2 linux/amd64` 환경에서 다음 대표 예제를 임시 module로 컴파일 검증했다.

- `github.com/openai/openai-go v1.12.0` Responses API 예제: `go test ./...` 통과.
- `github.com/sst/opencode-sdk-go v0.19.2` session list 예제: `go test ./...` 통과.

비공식 `github.com/picatz/openai/codex` 예제는 package API 안정성이 낮아 pseudo-real 예시로 남겼다. 실제 도입 전 `pkg.go.dev`와 repository examples 기준으로 재검증해야 한다.
