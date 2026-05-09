package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

func FileTools(ws *workspace.Workspace) ([]llm.Tool, llm.ToolRegistry) {
	strict := true
	defs := []llm.Tool{
		{Kind: llm.ToolFunction, Name: "file_read", Description: "workspace 파일을 읽어요. offset_line, limit_lines, max_bytes로 범위를 줄일 수 있어요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"path": stringSchema(), "offset_line": nonNegativeIntegerSchema(), "limit_lines": nonNegativeIntegerSchema(), "max_bytes": nonNegativeIntegerSchema()}, []string{"path"})},
		{Kind: llm.ToolFunction, Name: "file_write", Description: "workspace 파일을 써요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"path": stringSchema(), "content": stringSchema()}, []string{"path", "content"})},
		{Kind: llm.ToolFunction, Name: "file_delete", Description: "workspace 파일이나 디렉터리를 삭제해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"path": stringSchema(), "recursive": booleanSchema()}, []string{"path"})},
		{Kind: llm.ToolFunction, Name: "file_move", Description: "workspace 파일이나 디렉터리를 이동하거나 이름을 바꿔요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"source": stringSchema(), "destination": stringSchema(), "overwrite": booleanSchema()}, []string{"source", "destination"})},
		{Kind: llm.ToolFunction, Name: "file_edit", Description: "workspace 파일에서 old 텍스트를 new 텍스트로 교체해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"path": stringSchema(), "old": stringSchema(), "new": stringSchema(), "expected_replacements": nonNegativeIntegerSchema()}, []string{"path", "old", "new"})},
		{Kind: llm.ToolFunction, Name: "file_apply_patch", Description: "apply_patch 형식 patch를 workspace에 적용해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"patch_text": stringSchema()}, []string{"patch_text"})},
		{Kind: llm.ToolFunction, Name: "file_restore_checkpoint", Description: "workspace file checkpoint를 복구해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"checkpoint_id": stringSchema()}, []string{"checkpoint_id"})},
		{Kind: llm.ToolFunction, Name: "file_prune_checkpoints", Description: "최신 workspace file checkpoint만 남기고 오래된 snapshot을 삭제해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"keep_latest": nonNegativeIntegerSchema()}, []string{"keep_latest"})},
		{Kind: llm.ToolFunction, Name: "file_list", Description: "workspace 디렉터리를 나열해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"path": stringSchema(), "limit": nonNegativeIntegerSchema()}, []string{"path"})},
		{Kind: llm.ToolFunction, Name: "file_glob", Description: "workspace 파일 경로를 glob 패턴으로 찾아요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"pattern": stringSchema(), "limit": nonNegativeIntegerSchema()}, []string{"pattern"})},
		{Kind: llm.ToolFunction, Name: "file_grep", Description: "workspace 파일에서 문자열 또는 regex를 검색해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"pattern": stringSchema(), "path_glob": stringSchema(), "regex": booleanSchema(), "case_sensitive": booleanSchema(), "max_matches": nonNegativeIntegerSchema()}, []string{"pattern"})},
		{Kind: llm.ToolFunction, Name: "shell_run", Description: "workspace command를 실행하고 구조화 결과를 돌려줘요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"command": stringSchema(), "args": arraySchema(stringSchema()), "timeout_ms": nonNegativeIntegerSchema()}, []string{"command"})},
	}
	handlers := llm.ToolRegistry{
		"file_read": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path       string `json:"path"`
			OffsetLine int    `json:"offset_line"`
			LimitLines int    `json:"limit_lines"`
			MaxBytes   int    `json:"max_bytes"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			return ws.ReadFileRange(in.Path, workspace.ReadOptions{OffsetLine: in.OffsetLine, LimitLines: in.LimitLines, MaxBytes: in.MaxBytes})
		}),
		"file_write": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			cp, err := ws.CreateCheckpoint([]string{in.Path})
			if err != nil {
				return "", err
			}
			if err := ws.WriteFile(in.Path, in.Content); err != nil {
				return "", err
			}
			return "파일을 썼어요: " + in.Path + "\ncheckpoint_id: " + cp.ID, nil
		}),
		"file_delete": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			cp, err := ws.CreateCheckpoint([]string{in.Path})
			if err != nil {
				return "", err
			}
			if err := ws.DeletePath(in.Path, in.Recursive); err != nil {
				return "", err
			}
			return "경로를 삭제했어요: " + in.Path + "\ncheckpoint_id: " + cp.ID, nil
		}),
		"file_move": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
			Overwrite   bool   `json:"overwrite"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			cp, err := ws.CreateCheckpoint([]string{in.Source, in.Destination})
			if err != nil {
				return "", err
			}
			if err := ws.MovePath(in.Source, in.Destination, in.Overwrite); err != nil {
				return "", err
			}
			return "경로를 이동했어요: " + in.Source + " -> " + in.Destination + "\ncheckpoint_id: " + cp.ID, nil
		}),
		"file_edit": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path                 string `json:"path"`
			Old                  string `json:"old"`
			New                  string `json:"new"`
			ExpectedReplacements int    `json:"expected_replacements"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			cp, err := ws.CreateCheckpoint([]string{in.Path})
			if err != nil {
				return "", err
			}
			if err := ws.EditFile(in.Path, in.Old, in.New, in.ExpectedReplacements); err != nil {
				return "", err
			}
			return "파일 텍스트를 교체했어요: " + in.Path + "\ncheckpoint_id: " + cp.ID, nil
		}),
		"file_apply_patch": llm.JSONToolHandler(func(ctx context.Context, in struct {
			PatchText string `json:"patch_text"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			paths, err := ws.PatchPaths(in.PatchText)
			if err != nil {
				return "", err
			}
			cp, err := ws.CreateCheckpoint(paths)
			if err != nil {
				return "", err
			}
			if err := ws.ApplyPatch(in.PatchText); err != nil {
				return "", err
			}
			return "patch를 적용했어요\ncheckpoint_id: " + cp.ID, nil
		}),
		"file_restore_checkpoint": llm.JSONToolHandler(func(ctx context.Context, in struct {
			CheckpointID string `json:"checkpoint_id"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			cp, err := ws.RestoreCheckpoint(in.CheckpointID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("checkpoint를 복구했어요: %s (%d entries)", cp.ID, len(cp.Entries)), nil
		}),
		"file_prune_checkpoints": llm.JSONToolHandler(func(ctx context.Context, in struct {
			KeepLatest int `json:"keep_latest"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			result, err := ws.PruneCheckpoints(in.KeepLatest)
			if err != nil {
				return "", err
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			return string(b), nil
		}),
		"file_list": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Path  string `json:"path"`
			Limit int    `json:"limit"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			xs, err := ws.List(in.Path)
			if err != nil {
				return "", err
			}
			xs, truncated, err := limitStringList(xs, in.Limit, workspace.MaxListEntries, "limit")
			if err != nil {
				return "", err
			}
			return joinStringList(xs, truncated), nil
		}),
		"file_glob": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Pattern string `json:"pattern"`
			Limit   int    `json:"limit"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			xs, err := ws.Glob(in.Pattern)
			if err != nil {
				return "", err
			}
			xs, truncated, err := limitStringList(xs, in.Limit, workspace.MaxGlobMatches, "limit")
			if err != nil {
				return "", err
			}
			return joinStringList(xs, truncated), nil
		}),
		"file_grep": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Pattern       string `json:"pattern"`
			PathGlob      string `json:"path_glob"`
			Regex         bool   `json:"regex"`
			CaseSensitive bool   `json:"case_sensitive"`
			MaxMatches    int    `json:"max_matches"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			matches, err := ws.Grep(in.Pattern, workspace.GrepOptions{PathGlob: in.PathGlob, Regex: in.Regex, CaseSensitive: in.CaseSensitive, MaxMatches: in.MaxMatches})
			if err != nil {
				return "", err
			}
			b, _ := json.MarshalIndent(matches, "", "  ")
			return string(b), nil
		}),
		"shell_run": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Command   string   `json:"command"`
			Args      []string `json:"args"`
			TimeoutMS int      `json:"timeout_ms"`
		}) (string, error) {
			if ws == nil {
				return "", fmt.Errorf("workspace is nil")
			}
			res, err := ws.RunDetailed(ctx, in.Command, in.Args, workspace.CommandOptions{Timeout: time.Duration(in.TimeoutMS) * time.Millisecond})
			b, _ := json.MarshalIndent(res, "", "  ")
			if err != nil && !res.IsProcessOutcome(err) {
				return "", err
			}
			return string(b), nil
		}),
	}
	return defs, handlers
}

func limitStringList(items []string, limit int, maxLimit int, label string) ([]string, bool, error) {
	if limit < 0 {
		return nil, false, fmt.Errorf("%s must be >= 0", label)
	}
	if limit > maxLimit {
		return nil, false, fmt.Errorf("%s must be <= %d", label, maxLimit)
	}
	if limit == 0 || len(items) <= limit {
		return items, false, nil
	}
	return items[:limit], true, nil
}

func joinStringList(items []string, truncated bool) string {
	if !truncated {
		return strings.Join(items, "\n")
	}
	out := append([]string{}, items...)
	out = append(out, "[result_truncated]")
	return strings.Join(out, "\n")
}
