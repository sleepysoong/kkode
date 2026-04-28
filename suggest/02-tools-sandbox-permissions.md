# 02. Tools, permissions, sandbox, checkpoint 강화 제안

작성일: 2026-04-27

## 결론

`kkode`의 workspace tool은 이제 최소 동작은 해요. 하지만 실사용 coding agent에는 아직 부족해요.

가장 큰 결핍은 아래예요.

1. permission이 너무 단순해요.
2. OS-level sandbox가 없어요.
3. patch/checkpoint/undo가 없어요.
4. code intelligence tool이 없어요.
5. web/question/todo/custom/MCP tool surface가 부족해요.
6. command 결과가 구조화되어 있지 않아요.

## 비교 대상의 핵심 기능

OpenCode tools 문서는 built-in tool로 `bash`, `edit`, `write`, `read`, `grep`, `glob`, `lsp`, `apply_patch`, `skill`, `todowrite`, `webfetch`, `websearch`, `question`을 설명해요. 특히 `read`는 큰 파일용 line range를 지원하고, `grep/glob`는 ripgrep과 `.gitignore`를 활용해요. `lsp`는 definition, references, hover, symbols, call hierarchy를 제공해요.

Claude Code permissions 문서는 read-only tool은 기본 승인 없이, bash와 file modification은 승인 대상으로 다루며 deny -> ask -> allow 순서의 rule 평가를 설명해요. sandboxing 문서는 filesystem/network isolation과 OS-level enforcement를 강조해요. checkpointing 문서는 file editing tool 변경을 checkpoint로 자동 추적하고 rewind에서 code/conversation restore를 분리해요.

Codex config reference는 `approval_policy`, `sandbox_mode`, workspace-write writable roots, network access, protected paths, MCP tool approvals를 다뤄요.

## 현재 `workspace` tool 평가

| 현재 tool | 평가 | 보완 |
|---|---|---|
| `workspace_read_file` | 파일 전체만 읽어요 | line range, max bytes, binary 감지 필요해요 |
| `workspace_write_file` | 전체 overwrite라 위험해요 | create-only/overwrite 분리, checkpoint 필요해요 |
| `workspace_replace_in_file` | 첫 match만 교체해요 | 다중 match 정책, expected count 필요해요 |
| `workspace_list` | 단순 `os.ReadDir`예요 | recursive glob, mtime sort 필요해요 |
| `workspace_search` | literal search예요 | regex, file glob, ripgrep, ignore policy 필요해요 |
| `workspace_run_command` | command prefix만 봐요 | shell parser, env policy, timeout, stderr/exit code 필요해요 |

## Tool surface 제안

### P0 tool

```text
workspace_read_file(path, offset_line?, limit_lines?, max_bytes?)
workspace_list(path, recursive?, max_entries?)
workspace_glob(pattern, include_ignored?)
workspace_grep(pattern, path_glob?, regex?, case_sensitive?, include_ignored?)
workspace_apply_patch(patch_text)
workspace_edit(path, old, new, expected_replacements?)
workspace_write_file(path, content, overwrite?)
workspace_run_command(command, args, timeout_ms?, env?)
workspace_diagnostics(path?)
workspace_lsp(operation, path, line, character, query?)
todo_write(items)
question(header, question, options)
```

### P1 tool

```text
web_fetch(url, max_bytes?)
web_search(query, recency?, domains?)
workspace_move_file(old_path, new_path)
workspace_delete_file(path)
git_status()
git_diff(path?)
git_apply_reverse(checkpoint_id)
```

## Permission engine 설계

현재 `llm.ApprovalPolicy`는 mode와 allowed commands/paths만 있어요. 이를 호환 유지하면서 별도 `permission` package로 확장하는 게 좋아요.

```go
type Action string
const (
    ActionAllow Action = "allow"
    ActionAsk   Action = "ask"
    ActionDeny  Action = "deny"
)

type Request struct {
    SessionID string
    Tool      string
    Args      map[string]any
    Command   string
    Path      string
    URL       string
    AgentName string
}

type Decision struct {
    Action Action
    Reason string
    RuleID string
}

type Engine interface {
    Decide(ctx context.Context, req Request) (Decision, error)
}
```

Rule syntax는 OpenCode/Claude Code 스타일을 섞으면 좋아요.

```jsonc
{
  "permission": {
    "*": "ask",
    "read": "allow",
    "grep": "allow",
    "glob": "allow",
    "edit": {
      "*": "ask",
      "docs/**/*.md": "allow",
      ".git/**": "deny"
    },
    "bash": {
      "go test *": "allow",
      "go vet *": "allow",
      "rm *": "deny",
      "git push *": "ask"
    },
    "webfetch": {
      "domain:pkg.go.dev": "allow",
      "*": "ask"
    }
  }
}
```

평가 순서는 이렇게 해야해요.

1. managed deny
2. project deny
3. user deny
4. exact ask
5. exact allow
6. wildcard ask/allow
7. default

Deny가 항상 우선해야해요.

## Protected path 제안

다음 경로는 기본적으로 쓰기 전 확인해야해요.

```text
.git/**
.github/workflows/**
.env
.env.*
*.pem
*.key
.ssh/**
.codex/**
.claude/**
.opencode/**
.vscode/**
.idea/**
.husky/**
```

Claude Code는 bypass permission에서도 `.git`, `.claude`, editor config, git hooks 계열을 보호한다고 설명해요. Codex도 protected paths와 writable roots를 분리해요. 우리도 단순 path allow만으로는 부족해요.

## OS sandbox 제안

Go에서 OS sandbox는 플랫폼별로 다르게 구현해야해요.

### Linux

- `bubblewrap`를 우선 지원해요.
- workspace root만 rw bind mount해요.
- `/tmp`는 session별 temp dir로 제한해요.
- network는 기본 off 또는 proxy allowlist를 둬요.

### macOS

- Seatbelt profile을 생성해서 `sandbox-exec` 계열을 검토해요.
- 최신 macOS에서 sandbox-exec 지원 상태를 별도 확인해야해요.
- 최소한 subprocess cwd/path guard + network proxy guard를 먼저 구현해요.

### Windows

- WSL2 우선 지원을 명시해요.
- native Windows는 Job Object + ACL 기반 제한을 장기 과제로 둬요.

```go
type SandboxConfig struct {
    Mode          SandboxMode // read_only, workspace_write, danger_full_access
    WritableRoots []string
    ReadableRoots []string
    Network       NetworkPolicy
    Env           EnvPolicy
}

type Executor interface {
    Run(ctx context.Context, req CommandRequest) (*CommandResult, error)
}
```

## Command result 구조화

현재 `Run`은 stdout string만 반환하고 error에 stderr를 붙여요. 모델과 UI가 쓰기 어렵습니다.

```go
type CommandResult struct {
    Command   []string
    CWD       string
    ExitCode  int
    Stdout    string
    Stderr    string
    StartedAt time.Time
    EndedAt   time.Time
    TimedOut  bool
}
```

Tool output은 사람이 읽는 summary + raw JSON 둘 다 보존해야해요.

## Checkpoint/undo 설계

Claude Code checkpointing의 핵심은 “file editing tool 변경은 자동 추적하고, bash command 변경은 추적 한계가 있다”는 점이에요. 우리도 이 경계를 명확히 해야해요.

```go
type Checkpoint struct {
    ID        string
    SessionID string
    TurnID    string
    Before    []FileSnapshot
    After     []FileSnapshot
    CreatedAt time.Time
}

type FileSnapshot struct {
    Path   string
    SHA256 string
    Mode   fs.FileMode
    Bytes  []byte // size limit 초과 시 object store 참조로 바꿔요
}
```

`workspace_apply_patch`, `workspace_edit`, `workspace_write_file`, `workspace_delete_file`, `workspace_move_file`은 실행 전후 snapshot을 남겨야해요.

CLI는 이렇게 가요.

```bash
kkode undo
kkode redo
kkode rewind --session <id>
kkode checkpoint list
kkode checkpoint restore <id> --code-only
```

## LSP tool 설계

OpenCode LSP 문서가 보여주는 기능을 Go에서 시작하려면 `gopls`부터 붙이면 돼요.

```go
type LSPClient interface {
    Initialize(ctx context.Context, root string) error
    Diagnostics(ctx context.Context, path string) ([]Diagnostic, error)
    Definition(ctx context.Context, path string, line, col int) ([]Location, error)
    References(ctx context.Context, path string, line, col int) ([]Location, error)
    Hover(ctx context.Context, path string, line, col int) (*Hover, error)
    DocumentSymbols(ctx context.Context, path string) ([]Symbol, error)
    WorkspaceSymbols(ctx context.Context, query string) ([]Symbol, error)
}
```

초기 구현은 `gopls`만 지원해도 프로젝트 방향과 잘 맞아요.

## 테스트 제안

- deny rule이 allow rule보다 우선하는지 테스트해요.
- `.git/config` write가 default deny인지 테스트해요.
- `workspace_apply_patch` rollback이 되는지 테스트해요.
- `rm -rf`가 command parser에서 막히는지 테스트해요.
- line range read가 max bytes를 지키는지 테스트해요.
- LSP diagnostics가 broken Go file에서 에러를 반환하는지 테스트해요.


## 구현 상태: 2026-04-28

이번 구현으로 아래가 완료됐어요.

- `permission/` 패키지에 `Action`, `Request`, `Decision`, `Rule`, `Engine`, `StaticEngine`을 추가했어요.
- rule 평가는 `deny -> ask -> allow -> default` 순서로 동작해요. 현재 non-interactive workspace에서는 `allow`만 실행하고 `ask/deny`는 차단해요.
- `workspace.NewWithPermission`으로 permission engine을 붙일 수 있어요.
- `workspace_read_file`에 `offset_line`, `limit_lines`, `max_bytes` 옵션을 추가했어요.
- `workspace_glob`, `workspace_grep`, `workspace_apply_patch` tool을 추가했어요.
- `workspace_replace_in_file`은 `expected_replacements`를 지원하는 `EditFile` 위로 올렸어요.
- `workspace_run_command`는 `CommandResult` JSON으로 command, cwd, exit code, stdout, stderr, timeout 여부를 돌려줘요.
- protected path write 차단을 추가했어요. `.git/**`, `.env*`, `.claude/**`, `.codex/**` 같은 경로는 기본적으로 write/apply_patch가 막혀요.

아직 남은 것은 아래예요.

- `ask` permission을 실제 TUI/CLI 승인 UX와 연결하지 않았어요.
- OS-level sandbox(bubblewrap/seatbelt)는 아직 없어요.
- checkpoint snapshot/undo/redo는 아직 없어요.
- LSP diagnostics/tool은 아직 없어요.
- `workspace_apply_patch`는 Codex 스타일의 단순 apply_patch grammar만 지원하고, 전체 unified diff parser는 아니에요.


## 구현 상태 업데이트: 2026-04-28 YOLO 전환

사용자 지시에 따라 방금 추가했던 `permission/` 패키지와 deny/ask/allow rule engine은 제거했어요. 현재 `cmd/kkode-agent` 기본값은 YOLO 모드이며 `llm.ApprovalAllowAll`로 파일 쓰기와 shell 실행을 바로 허용해요.

남긴 것:

- `workspace_read_file` 범위 옵션
- `workspace_glob`
- `workspace_grep`
- `workspace_apply_patch`
- `workspace_replace_in_file.expected_replacements`
- 구조화 `CommandResult`

제거한 것:

- `permission/` 패키지
- `workspace.NewWithPermission`
- protected path write 차단
- ask/deny/allow rule 평가

주의: YOLO 모드는 빠른 구현 검증용이에요. 안전 모드가 다시 필요해지면 permission engine보다 checkpoint/undo와 UI 승인 흐름을 먼저 설계해야해요.

## 참고 소스

- OpenCode Tools: https://opencode.ai/docs/tools/
- OpenCode Permissions: https://opencode.ai/docs/permissions/
- OpenCode LSP: https://opencode.ai/docs/lsp/
- Claude Code Permissions: https://code.claude.com/docs/en/permissions
- Claude Code Sandboxing: https://code.claude.com/docs/en/sandboxing
- Claude Code Checkpointing: https://code.claude.com/docs/en/checkpointing
- OpenAI Codex Config Reference: https://developers.openai.com/codex/config-reference
