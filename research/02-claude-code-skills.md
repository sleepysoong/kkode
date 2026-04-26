# 02. Claude Code Skills 원리 및 구조

작성일: 2026-04-26

## 핵심 결론

Claude Code의 Skill은 **`SKILL.md`를 entrypoint로 하는 디렉터리 패키지**다. 단순 slash command보다 더 구조화된 재사용 능력 패키지이며, Claude가 자동으로 로딩할 수도 있고 사용자가 `/skill-name`으로 직접 호출할 수도 있다.

핵심 구성:

```text
my-skill/
├── SKILL.md           # 필수: YAML frontmatter + markdown instructions
├── references/        # 선택: 세부 문서
├── assets/            # 선택: 템플릿, 이미지, 데이터
├── examples/          # 선택: 입력/출력 예시
└── scripts/           # 선택: Claude가 실행할 스크립트
```

## 작동 원리: progressive disclosure

Claude Skills의 핵심은 **progressive disclosure**다.

1. Claude는 모든 skill의 전체 본문을 항상 읽지 않는다.
2. 먼저 skill 목록의 `name`/`description` 같은 메타데이터로 “언제 쓸지”를 판단한다.
3. 요청이 skill description과 맞으면 `SKILL.md` 본문을 컨텍스트에 로드한다.
4. `SKILL.md`가 추가 파일(`references/foo.md`, `scripts/bar.py`)을 안내하면 필요한 시점에만 읽거나 실행한다.
5. 따라서 많은 skill을 설치해도 전체 reference 문서가 한꺼번에 context window를 잡아먹지 않는다.

Claude Code 공식 문서는 skill이 호출되면 렌더링된 `SKILL.md` 내용이 대화에 들어가고, 이후 세션 동안 남는다고 설명한다. 그래서 `SKILL.md`는 한 번만 수행할 지시가 아니라 “그 작업 동안 계속 지켜야 하는 규칙”도 포함해야 한다.

## 위치와 우선순위

Claude Code에서 skill 위치는 scope를 결정한다.

| 위치 | 경로 | 적용 범위 |
|---|---|---|
| Personal | `~/.claude/skills/<skill-name>/SKILL.md` | 모든 프로젝트 |
| Project | `.claude/skills/<skill-name>/SKILL.md` | 현재 프로젝트 |
| Plugin | `<plugin>/skills/<skill-name>/SKILL.md` | 플러그인이 활성화된 곳 |
| Enterprise | managed settings | 조직 전체 |

공식 문서 기준으로 같은 이름이 충돌하면 enterprise > personal > project 순으로 우선한다. Plugin skill은 `plugin-name:skill-name` namespace를 사용해서 일반 skill과 충돌하지 않게 한다.

## SKILL.md 기본 형식

```markdown
---
name: explain-code
description: Explain code using diagrams and analogies. Use when the user asks how code works or requests a conceptual explanation.
---

# Explain Code

## Workflow

1. Identify the entrypoint and core data flow.
2. Explain the code in plain language.
3. Include a small ASCII diagram.
4. Mention edge cases and assumptions.

## Output format

- Summary
- Flow diagram
- Key functions/files
- Risks or TODOs
```

### frontmatter 주요 필드

Claude Code 공식 docs 기준:

```yaml
---
name: my-skill
description: What this skill does and when to use it
disable-model-invocation: true
user-invocable: false
allowed-tools: Read Grep
context: fork
---
```

중요 필드:

- `name`
  - skill 이름.
  - slash command 이름이 된다: `/my-skill`.
- `description`
  - Claude가 자동으로 이 skill을 사용할지 판단하는 핵심 문장.
  - “무엇을 하는지”보다 “언제 써야 하는지”가 중요하다.
- `disable-model-invocation: true`
  - Claude가 자동으로 실행하지 못하고, 사용자만 직접 호출하게 만든다.
  - 배포, 결제, destructive action, commit/push처럼 타이밍 통제가 필요한 workflow에 적합.
- `user-invocable: false`
  - 사용자가 slash command로 직접 부르지 못하게 하고 Claude의 배경지식처럼 쓰게 한다.
- `allowed-tools`
  - skill 활성화 중 사전 승인할 tool 목록.
  - “허용 목록만 사용할 수 있다”는 뜻은 아니다. 기본 permission 정책은 계속 적용된다.
- `context: fork`
  - 복잡한 skill을 subagent/forked context에서 실행하는 패턴에 사용.

## 좋은 description 작성법

나쁜 예:

```yaml
description: Helps with docs
```

좋은 예:

```yaml
description: Use when writing or updating public API documentation. Enforces endpoint summary, parameter tables, error examples, and changelog notes.
```

왜 좋은가:

- trigger condition이 구체적이다.
- 산출물/검증 기준이 들어 있다.
- 비슷한 skill과 구분 가능하다.

## 단일 파일 skill 예제

```text
commit-helper/
└── SKILL.md
```

`SKILL.md`:

```markdown
---
name: commit-helper
description: Generate clear git commit messages from staged diffs. Use when the user asks for a commit message or reviews staged changes.
disable-model-invocation: true
allowed-tools: Bash(git diff --staged *) Bash(git status *)
---

# Commit Helper

## Steps

1. Run `git status --short`.
2. Run `git diff --staged`.
3. Summarize the intent, not just changed files.
4. Produce a commit message under 72 chars for the title.
5. Add trailers only if they provide useful future context.

## Output

```text
<why this change was made>

<short body explaining constraints and tradeoffs>

Tested: <what was verified>
Not-tested: <known gaps>
```
```

## 다중 파일 skill 예제

```text
api-docs/
├── SKILL.md
├── references/
│   ├── error-format.md
│   └── style-guide.md
├── examples/
│   └── endpoint-doc-example.md
└── scripts/
    └── validate-openapi.py
```

`SKILL.md`:

```markdown
---
name: api-docs
description: Use when creating or updating REST API docs. Applies endpoint format, auth notes, error examples, and OpenAPI validation.
allowed-tools: Read Grep Glob Bash(python scripts/validate-openapi.py *)
---

# API Docs Skill

## Workflow

1. Read the target endpoint implementation and existing docs.
2. Apply the docs format from `references/style-guide.md` only when writing public docs.
3. Use `references/error-format.md` when documenting errors.
4. If an OpenAPI file changed, run `scripts/validate-openapi.py <path>`.
5. Compare the new page with `examples/endpoint-doc-example.md` before finalizing.

## Rules

- Always include auth requirements.
- Always include success and failure examples.
- Never invent undocumented status codes.
```

## scripts 사용 원칙

Skill에 script를 넣으면 Claude가 해당 script를 실행해 반복 작업을 안정적으로 처리할 수 있다.

좋은 script 조건:

- 입력을 인자로 받는다.
- 하드코딩된 절대경로를 피한다.
- 실패 시 exit code와 stderr를 명확히 낸다.
- 하나의 script는 하나의 일을 한다.
- 외부 의존성은 frontmatter 또는 README에 명시한다.

예:

```python
#!/usr/bin/env python3
import json
import sys
from pathlib import Path

if len(sys.argv) != 2:
    print("usage: validate-json.py <file>", file=sys.stderr)
    sys.exit(2)

path = Path(sys.argv[1])
try:
    json.loads(path.read_text())
except Exception as e:
    print(f"invalid json: {path}: {e}", file=sys.stderr)
    sys.exit(1)

print(f"ok: {path}")
```

## Claude Code skill 설계 체크리스트

- [ ] skill은 하나의 반복 가능한 workflow에 집중하는가?
- [ ] `description`에 “언제 쓰는지”가 명확한가?
- [ ] 위험한 side effect가 있으면 `disable-model-invocation: true`를 썼는가?
- [ ] `SKILL.md`는 500줄 이하로 유지했는가?
- [ ] 세부 reference는 별도 파일로 분리했는가?
- [ ] 추가 파일은 `SKILL.md`에서 언제 읽을지 안내했는가?
- [ ] 예시 입력/출력이 있는가?
- [ ] scripts는 작고 검증 가능하며 실패 메시지가 명확한가?
- [ ] dependency가 있으면 명시했는가?
- [ ] project/personal/plugin scope 중 어디에 둘지 결정했는가?

## Claude Skills와 slash command의 차이

| 항목 | Slash command | Skill |
|---|---|---|
| 기본 형태 | 보통 단일 markdown command | 디렉터리 패키지 + `SKILL.md` |
| 호출 | 대개 사용자가 직접 | 사용자 직접 + 모델 자동 가능 |
| 추가 파일 | 제한적 | references/assets/scripts/examples 구조화 가능 |
| 자동 라우팅 | 약함 | `description` 기반 자동 로딩 |
| 대규모 지식 | 부적합 | progressive disclosure에 적합 |

## 소스 검증

- Claude Code Skills docs: https://code.claude.com/docs/en/skills  
  - `SKILL.md`가 YAML frontmatter와 markdown instructions로 구성됨 확인.
  - skill 위치, personal/project/plugin scope, live detection, nested discovery, supporting files, frontmatter fields 확인.
  - `allowed-tools`, `disable-model-invocation`, `user-invocable`, skill invocation 후 context 유지 동작 확인.
- Claude custom skills docs: https://claude.com/docs/skills/how-to  
  - directory structure, required `SKILL.md`, optional `scripts/`, `references/`, `assets/`, name/description 규칙, 500줄 권장 확인.
- Anthropic blog, Building Skills for Claude Code: https://www.claude.com/blog/building-skills-for-claude-code  
  - progressive disclosure, `description`이 load trigger로 작동한다는 설명 확인.
