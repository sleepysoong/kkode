# 11. Provider 변환 레이어와 source 확장 계획이에요

## 목표예요

`kkode`는 앞으로 OpenAI, Copilot, Codex, OmniRoute뿐 아니라 사내 gateway, 개인 proxy, OpenCode/OpenClaw류 local API, vendor별 HTTP endpoint를 계속 붙여야 해요. 그래서 핵심 흐름은 항상 아래처럼 유지해야 해요.

```text
llm.Request
  → RequestConverter
  → llm.ProviderRequest
  → ProviderCaller / ProviderStreamCaller
  → llm.ProviderResult / llm.EventStream
  → ResponseConverter
  → llm.Response
```

이 구조에서는 앱, gateway, agent loop가 외부 API payload를 직접 알지 않아도 돼요. 새 provider는 변환기와 source caller만 추가하면 돼요.

## 현재 구현된 확장 지점이에요

- `llm.RequestConverter`, `llm.ResponseConverter`가 표준 request/response와 provider payload 사이를 담당해요.
- `llm.ProviderCaller`, `llm.ProviderStreamCaller`가 실제 HTTP/SDK/CLI/source 호출 경계예요.
- `llm.ProviderPipeline`이 `Prepare → Call → Decode` 단계를 분리해요.
- `llm.AdaptedProvider`가 pipeline을 `llm.Provider`로 감싸요.
- `app.RegisterProvider`가 provider spec, conversion factory, source factory를 런타임 registry에 추가해요.
- `app.RegisterHTTPJSONProvider`와 `KKODE_HTTPJSON_PROVIDERS`가 OpenAI-compatible 파생 HTTP source를 재컴파일 없이 추가해요.
- `providers/httpjson.Caller`가 HTTP JSON source 호출, route mapping, retry, header merge, SSE raw stream을 재사용해요.

## 이번에 강화한 부분이에요

Gateway run/test 요청의 `metadata`도 provider `llm.Request.Metadata`까지 전달해요. 그래서 외부 웹 패널이나 Discord adapter가 넣은 `trace_id`, `deployment`, `api_version`, `account_id` 같은 값을 run record, provider metadata, HTTP route template에서 같은 값으로 재사용할 수 있어요.

HTTP JSON route가 고정 path만 지원하면 `/responses` 같은 OpenAI-compatible API에는 충분하지만, 아래 형태의 일반 API에는 부족해요.

```text
POST /v1/providers/{provider}/models/{model}/generate?api-version=2026-05-07
POST /deployments/{deployment}/chat/completions?api-version=...
POST /accounts/{account_id}/agents/{agent_id}/runs
```

그래서 `providers/httpjson.Route`와 `app.ProviderRouteSpec`에 `Query map[string]string`를 추가하고, `Path`/`Query` template를 지원해요.

지원 template는 다음과 같아요.

- `{model}`: `llm.ProviderRequest.Model` 값이에요.
- `{operation}`: `llm.ProviderRequest.Operation` 값이에요.
- `{metadata.key}`: `llm.ProviderRequest.Metadata["key"]` 값이에요.
- `{key}`: 짧게 쓴 `llm.ProviderRequest.Metadata["key"]` 값이에요.

path 값은 `url.PathEscape`를 적용해요. 그래서 `claude/sonnet` 같은 model 값도 path segment를 깨지 않아요. query 값은 `url.Values.Encode`로 인코딩해요.

## 사용 예제예요

```go
caller := httpjson.New(httpjson.Config{
    ProviderName:     "templated-api",
    BaseURL:          "https://api.example.com",
    DefaultOperation: "model.generate",
    Routes: map[string]httpjson.Route{
        "model.generate": {
            Method: http.MethodPost,
            Path:   "/v1/providers/{provider}/models/{model}/generate",
            Query: map[string]string{
                "api-version": "{metadata.api_version}",
                "trace":       "{trace_id}",
            },
        },
    },
})

pipeline := llm.ProviderPipeline{
    ProviderName: "templated-api",
    RequestConverter: llm.RequestConverterFunc(func(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
        return llm.ProviderRequest{
            Operation: opts.Operation,
            Model:     req.Model,
            Body:      map[string]any{"prompt": req.Messages[0].Content},
            Metadata: map[string]string{
                "provider":    req.Metadata["provider"],
                "api_version": req.Metadata["api_version"],
                "trace_id":    req.Metadata["trace_id"],
            },
        }, nil
    }),
    Caller: caller,
    ResponseConverter: llm.ResponseConverterFunc(func(ctx context.Context, result llm.ProviderResult) (*llm.Response, error) {
        return parseTemplatedAPIResponse(result.Body, result.Provider, result.Model)
    }),
    Options: llm.ConvertOptions{Operation: "model.generate"},
}
```

## 앞으로 지켜야 할 규칙이에요

1. 앱과 gateway는 provider payload를 직접 만들지 않아야 해요.
2. provider-specific 값은 `ProviderRequest.Body`, `Headers`, `Metadata`, `Raw`에 격리해야 해요.
3. HTTP API 차이는 가능하면 `httpjson.Route`와 converter metadata로 해결해야 해요.
4. HTTP로 표현할 수 없는 SDK/session/subprocess 차이는 `ProviderCaller`나 `ProviderStreamCaller` 구현체로 숨겨야 해요.
5. 권한/승인 레이어는 만들지 않아요. source caller는 요청을 받으면 바로 실행해야 해요.
6. 새 provider를 넣을 때는 `ProviderSpecs` discovery, preview, gateway provider test가 같은 conversion profile을 보게 해야 해요.

## 검증해야 하는 테스트예요

- `go test ./providers/httpjson`로 route template, query template, 누락 값 오류를 확인해요.
- `go test ./app`로 registry 등록과 방어 복사를 확인해요.
- `go test ./cmd/kkode-gateway ./gateway`로 provider discovery DTO와 OpenAPI schema를 확인해요.
- 전체 변경 후에는 `go test ./...`, `go vet ./...`, `staticcheck`, race test를 실행해야 해요.
