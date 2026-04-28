package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/omniroute"
	"github.com/sleepysoong/kkode/providers/openai"
	agentruntime "github.com/sleepysoong/kkode/runtime"
	"github.com/sleepysoong/kkode/session"
	"github.com/sleepysoong/kkode/transcript"
	"github.com/sleepysoong/kkode/workspace"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "오류가 났어요:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kkode-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", envDefault("KKODE_PROVIDER", "openai"), "사용할 provider예요: openai, omniroute, copilot, codex")
	model := fs.String("model", os.Getenv("KKODE_MODEL"), "사용할 모델 이름이에요")
	root := fs.String("root", envDefault("KKODE_ROOT", "."), "agent가 접근할 workspace root예요")
	instructions := fs.String("instructions", os.Getenv("KKODE_INSTRUCTIONS"), "agent system/developer instructions예요")
	write := fs.Bool("write", envBool("KKODE_WRITE"), "호환용 flag예요. 현재 기본은 YOLO라 항상 쓰기를 허용해요")
	readOnly := fs.Bool("read-only", envBool("KKODE_READ_ONLY"), "YOLO를 끄고 읽기 전용 workspace로 실행해요")
	commands := fs.String("commands", os.Getenv("KKODE_ALLOWED_COMMANDS"), "호환용 command allowlist예요. YOLO 모드에서는 비어 있어도 명령을 실행해요")
	maxIterations := fs.Int("max-iterations", envInt("KKODE_MAX_ITERATIONS", 8), "tool loop 최대 반복 횟수예요")
	reasoningEffort := fs.String("reasoning-effort", os.Getenv("KKODE_REASONING_EFFORT"), "OpenAI 호환 reasoning effort예요")
	reasoningSummary := fs.String("reasoning-summary", os.Getenv("KKODE_REASONING_SUMMARY"), "OpenAI 호환 reasoning summary 설정이에요")
	include := fs.String("include", os.Getenv("KKODE_INCLUDE"), "Responses API include 값을 쉼표로 적어요")
	blockedInput := fs.String("blocked-input", os.Getenv("KKODE_BLOCKED_INPUT"), "입력 guardrail substring을 쉼표로 적어요")
	blockedOutput := fs.String("blocked-output", os.Getenv("KKODE_BLOCKED_OUTPUT"), "출력 guardrail substring을 쉼표로 적어요")
	transcriptPath := fs.String("transcript", os.Getenv("KKODE_TRANSCRIPT"), "대화 transcript JSON을 저장할 경로예요")
	statePath := fs.String("state", envDefault("KKODE_STATE_DB", ".kkode/state.db"), "SQLite session state DB 경로예요")
	sessionID := fs.String("session", os.Getenv("KKODE_SESSION_ID"), "이어갈 session ID예요")
	forkSessionID := fs.String("fork-session", os.Getenv("KKODE_FORK_SESSION_ID"), "fork할 원본 session ID예요")
	forkAtTurnID := fs.String("fork-at", os.Getenv("KKODE_FORK_AT_TURN_ID"), "fork 기준 turn ID예요")
	listSessions := fs.Bool("list-sessions", false, "SQLite DB에 저장된 session 목록을 출력해요")
	noSession := fs.Bool("no-session", envBool("KKODE_NO_SESSION"), "SQLite session 저장을 끄고 단발 실행해요")
	redactTranscript := fs.Bool("redact-transcript", envBool("KKODE_REDACT_TRANSCRIPT"), "transcript 저장 전에 secret 패턴을 마스킹해요")
	verbose := fs.Bool("v", envBool("KKODE_VERBOSE"), "trace event를 stderr에 출력해요")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *listSessions {
		store, err := session.OpenSQLite(*statePath)
		if err != nil {
			return err
		}
		defer store.Close()
		sessions, err := store.ListSessions(ctx, session.SessionQuery{Limit: 100})
		if err != nil {
			return err
		}
		for _, summary := range sessions {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%d\t%s\n", summary.ID, summary.ProviderName, summary.Model, summary.Mode, summary.TurnCount, summary.UpdatedAt.Format(time.RFC3339))
		}
		return nil
	}

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		return errors.New("prompt가 필요해요. 인자로 주거나 stdin으로 전달해야해요")
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	_ = *write
	policy := llm.ApprovalPolicy{Mode: llm.ApprovalAllowAll, AllowedCommands: csv(*commands), AllowedPaths: []string{absRoot}}
	if *readOnly {
		policy.Mode = llm.ApprovalReadOnly
	}
	ws, err := workspace.New(absRoot, policy)
	if err != nil {
		return err
	}

	provider, closeFn, err := buildProvider(*providerName, absRoot)
	if err != nil {
		return err
	}
	if closeFn != nil {
		defer closeFn()
	}
	if *model == "" {
		*model = defaultModel(*providerName)
	}

	tr := (*transcript.Transcript)(nil)
	if *transcriptPath != "" {
		tr = transcript.New("kkode-agent-" + time.Now().UTC().Format("20060102T150405Z"))
	}
	guardrails := agent.Guardrails{
		BlockedSubstrings:       csv(*blockedInput),
		BlockedOutputSubstrings: csv(*blockedOutput),
		RedactTranscript:        *redactTranscript,
	}
	base := llm.Request{Include: csv(*include)}
	if *reasoningEffort != "" || *reasoningSummary != "" {
		base.Reasoning = &llm.ReasoningConfig{Effort: *reasoningEffort, Summary: *reasoningSummary}
	}
	ag, err := agent.New(agent.Config{
		Provider:      provider,
		Model:         *model,
		Instructions:  *instructions,
		BaseRequest:   base,
		Workspace:     ws,
		MaxIterations: *maxIterations,
		Transcript:    tr,
		Guardrails:    guardrails,
		Observer: agent.ObserverFunc(func(ctx context.Context, event agent.TraceEvent) {
			if *verbose {
				fmt.Fprintf(stderr, "%s %s %s %s\n", event.At.Format(time.RFC3339), event.Type, event.Tool, firstNonEmpty(event.Error, event.Message))
			}
		}),
	})
	if err != nil {
		return err
	}
	var result *agent.RunResult
	if *noSession {
		result, err = ag.Run(ctx, prompt)
	} else {
		store, openErr := session.OpenSQLite(*statePath)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		rt := &agentruntime.Runtime{
			Store:           store,
			Agent:           ag,
			ProjectRoot:     absRoot,
			ProviderName:    provider.Name(),
			Model:           *model,
			AgentName:       "kkode-agent",
			Mode:            session.AgentModeBuild,
			MaxHistoryTurns: 8,
			EnableTodos:     true,
			Compaction: session.CompactionPolicy{
				Enabled:             true,
				TriggerTokenRatio:   0.85,
				PreserveFirstNTurns: 1,
				PreserveLastNTurns:  4,
			},
		}
		runResult, runErr := rt.Run(ctx, agentruntime.RunOptions{SessionID: *sessionID, ForkFrom: *forkSessionID, ForkAt: *forkAtTurnID, Prompt: prompt})
		err = runErr
		if runResult != nil {
			result = runResult.Agent
			fmt.Fprintf(stderr, "session: %s turn: %s\n", runResult.Session.ID, runResult.Turn.ID)
		}
	}
	if tr != nil {
		if saveErr := saveTranscript(tr, *transcriptPath, guardrails.RedactTranscript); saveErr != nil && err == nil {
			err = saveErr
		}
	}
	if result != nil && result.Response != nil && result.Response.Text != "" {
		fmt.Fprintln(stdout, result.Response.Text)
	}
	return err
}

func buildProvider(name, root string) (llm.Provider, func() error, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "openai-compatible":
		return openai.New(openai.Config{BaseURL: os.Getenv("OPENAI_BASE_URL"), APIKey: os.Getenv("OPENAI_API_KEY")}), nil, nil
	case "omniroute":
		return omniroute.New(omniroute.Config{BaseURL: os.Getenv("OMNIROUTE_BASE_URL"), APIKey: envDefault("OMNIROUTE_API_KEY", os.Getenv("OPENAI_API_KEY")), SessionID: os.Getenv("OMNIROUTE_SESSION_ID"), Progress: envBool("OMNIROUTE_PROGRESS")}), nil, nil
	case "copilot", "github-copilot":
		client := copilot.New(copilot.Config{WorkingDirectory: root, GitHubToken: envDefault("COPILOT_GITHUB_TOKEN", envDefault("GH_TOKEN", os.Getenv("GITHUB_TOKEN"))), ApproveAll: envBool("COPILOT_APPROVE_ALL")})
		return client, client.Close, nil
	case "codex", "codexcli", "codex-cli":
		return codexcli.New(codexcli.Config{WorkingDirectory: root, Sandbox: envDefault("CODEX_SANDBOX", "read-only"), Approval: envDefault("CODEX_APPROVAL", "never"), Ephemeral: envBool("CODEX_EPHEMERAL")}), nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown provider: %s", name)
	}
}

func defaultModel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "codexcli", "codex-cli":
		return "gpt-5.3-codex"
	default:
		return "gpt-5-mini"
	}
}

func saveTranscript(tr *transcript.Transcript, path string, redact bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if redact {
		return tr.SaveRedacted(path)
	}
	return tr.Save(path)
}

func csv(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes" || value == "y" || value == "on"
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
