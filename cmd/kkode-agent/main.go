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
	"github.com/sleepysoong/kkode/app"
	"github.com/sleepysoong/kkode/llm"
	agentruntime "github.com/sleepysoong/kkode/runtime"
	"github.com/sleepysoong/kkode/session"
	"github.com/sleepysoong/kkode/transcript"
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
	providerName := fs.String("provider", app.EnvDefault("KKODE_PROVIDER", "openai"), "사용할 provider예요: openai, omniroute, copilot, codex")
	model := fs.String("model", os.Getenv("KKODE_MODEL"), "사용할 모델 이름이에요")
	root := fs.String("root", app.EnvDefault("KKODE_ROOT", "."), "agent가 접근할 workspace root예요")
	instructions := fs.String("instructions", os.Getenv("KKODE_INSTRUCTIONS"), "agent system/developer instructions예요")
	maxIterations := fs.Int("max-iterations", app.EnvInt("KKODE_MAX_ITERATIONS", 8), "tool loop 최대 반복 횟수예요")
	reasoningEffort := fs.String("reasoning-effort", os.Getenv("KKODE_REASONING_EFFORT"), "OpenAI 호환 reasoning effort예요")
	reasoningSummary := fs.String("reasoning-summary", os.Getenv("KKODE_REASONING_SUMMARY"), "OpenAI 호환 reasoning summary 설정이에요")
	include := fs.String("include", os.Getenv("KKODE_INCLUDE"), "Responses API include 값을 쉼표로 적어요")
	blockedInput := fs.String("blocked-input", os.Getenv("KKODE_BLOCKED_INPUT"), "입력 guardrail substring을 쉼표로 적어요")
	blockedOutput := fs.String("blocked-output", os.Getenv("KKODE_BLOCKED_OUTPUT"), "출력 guardrail substring을 쉼표로 적어요")
	transcriptPath := fs.String("transcript", os.Getenv("KKODE_TRANSCRIPT"), "대화 transcript JSON을 저장할 경로예요")
	statePath := fs.String("state", app.EnvDefault("KKODE_STATE_DB", ".kkode/state.db"), "SQLite session state DB 경로예요")
	sessionID := fs.String("session", os.Getenv("KKODE_SESSION_ID"), "이어갈 session ID예요")
	forkSessionID := fs.String("fork-session", os.Getenv("KKODE_FORK_SESSION_ID"), "fork할 원본 session ID예요")
	forkAtTurnID := fs.String("fork-at", os.Getenv("KKODE_FORK_AT_TURN_ID"), "fork 기준 turn ID예요")
	listSessions := fs.Bool("list-sessions", false, "SQLite DB에 저장된 session 목록을 출력해요")
	noSession := fs.Bool("no-session", app.EnvBool("KKODE_NO_SESSION"), "SQLite session 저장을 끄고 단발 실행해요")
	redactTranscript := fs.Bool("redact-transcript", app.EnvBool("KKODE_REDACT_TRANSCRIPT"), "transcript 저장 전에 secret 패턴을 마스킹해요")
	noWeb := fs.Bool("no-web", app.EnvBool("KKODE_NO_WEB"), "web_fetch tool을 비활성화해요")
	webMaxBytes := fs.Int64("web-max-bytes", app.EnvInt64("KKODE_WEB_MAX_BYTES", 1<<20), "web_fetch가 읽을 최대 byte 수예요")
	verbose := fs.Bool("v", app.EnvBool("KKODE_VERBOSE"), "trace event를 stderr에 출력해요")
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

	ws, absRoot, err := app.NewWorkspace(app.WorkspaceOptions{Root: *root})
	if err != nil {
		return err
	}

	providerHandle, err := app.BuildProvider(*providerName, absRoot)
	if err != nil {
		return err
	}
	if providerHandle.Close != nil {
		defer providerHandle.Close()
	}
	provider := providerHandle.Provider
	if *model == "" {
		*model = app.DefaultModel(*providerName)
	}

	tr := (*transcript.Transcript)(nil)
	if *transcriptPath != "" {
		tr = transcript.New("kkode-agent-" + time.Now().UTC().Format("20060102T150405Z"))
	}
	guardrails := agent.Guardrails{
		BlockedSubstrings:       app.CSV(*blockedInput),
		BlockedOutputSubstrings: app.CSV(*blockedOutput),
		RedactTranscript:        *redactTranscript,
	}
	base := llm.Request{Include: app.CSV(*include)}
	if *reasoningEffort != "" || *reasoningSummary != "" {
		base.Reasoning = &llm.ReasoningConfig{Effort: *reasoningEffort, Summary: *reasoningSummary}
	}
	ag, err := app.NewAgent(provider, ws, app.AgentOptions{
		Model:         *model,
		Instructions:  *instructions,
		BaseRequest:   base,
		MaxIterations: *maxIterations,
		NoWeb:         *noWeb,
		WebMaxBytes:   *webMaxBytes,
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

func saveTranscript(tr *transcript.Transcript, path string, redact bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if redact {
		return tr.SaveRedacted(path)
	}
	return tr.Save(path)
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
