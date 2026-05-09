package gateway

import (
	"net/http"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// StatsResponse는 외부 dashboard adapter가 gateway 저장소 상태를 한 번에 그릴 때 쓰는 응답이에요.
type StatsResponse struct {
	Sessions              int                            `json:"sessions"`
	SessionsByProvider    map[string]int                 `json:"sessions_by_provider"`
	SessionsByModel       map[string]int                 `json:"sessions_by_model"`
	SessionsByMode        map[string]int                 `json:"sessions_by_mode"`
	Turns                 int                            `json:"turns"`
	Events                int                            `json:"events"`
	EventsByType          map[string]int                 `json:"events_by_type"`
	RunEvents             int                            `json:"run_events"`
	RunEventsByType       map[string]int                 `json:"run_events_by_type"`
	Todos                 int                            `json:"todos"`
	TodosByStatus         map[string]int                 `json:"todos_by_status"`
	Checkpoints           int                            `json:"checkpoints"`
	Artifacts             int                            `json:"artifacts"`
	ArtifactsByKind       map[string]int                 `json:"artifacts_by_kind"`
	ArtifactBytes         int64                          `json:"artifact_bytes"`
	ArtifactBytesByKind   map[string]int64               `json:"artifact_bytes_by_kind"`
	TotalRuns             int                            `json:"total_runs"`
	Runs                  map[string]int                 `json:"runs"`
	RunDuration           RunDurationStatsDTO            `json:"run_duration"`
	RunDurationByProvider map[string]RunDurationStatsDTO `json:"run_duration_by_provider"`
	RunDurationByModel    map[string]RunDurationStatsDTO `json:"run_duration_by_model"`
	RunUsage              UsageDTO                       `json:"run_usage"`
	RunUsageByProvider    map[string]UsageDTO            `json:"run_usage_by_provider"`
	RunUsageByModel       map[string]UsageDTO            `json:"run_usage_by_model"`
	TotalResources        int                            `json:"total_resources"`
	Resources             map[string]int                 `json:"resources"`
	ResourcesByEnabled    map[string]int                 `json:"resources_by_enabled"`
}

// RunDurationStatsDTO는 완료된 run timestamp에서 계산한 latency aggregate예요.
type RunDurationStatsDTO struct {
	Count int   `json:"count"`
	SumMS int64 `json:"sum_ms"`
	AvgMS int64 `json:"avg_ms"`
	MaxMS int64 `json:"max_ms"`
	P95MS int64 `json:"p95_ms"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeMethodNotAllowed(w, r, "지원하지 않는 stats 요청이에요", http.MethodGet)
		return
	}
	statsStore, ok := s.cfg.Store.(session.StatsStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "stats_store_missing", "이 gateway에는 StatsStore가 연결되지 않았어요")
		return
	}
	stats, err := statsStore.LoadStats(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "load_stats_failed", err.Error())
		return
	}
	writeJSON(w, statsResponseFromSession(stats))
}

func statsResponseFromSession(stats session.StoreStats) StatsResponse {
	return StatsResponse{
		Sessions:              stats.Sessions,
		SessionsByProvider:    cloneIntMap(stats.SessionsByProvider),
		SessionsByModel:       cloneIntMap(stats.SessionsByModel),
		SessionsByMode:        cloneIntMap(stats.SessionsByMode),
		Turns:                 stats.Turns,
		Events:                stats.Events,
		EventsByType:          cloneIntMap(stats.EventsByType),
		RunEvents:             stats.RunEvents,
		RunEventsByType:       cloneIntMap(stats.RunEventsByType),
		Todos:                 stats.Todos,
		TodosByStatus:         cloneIntMap(stats.TodosByStatus),
		Checkpoints:           stats.Checkpoints,
		Artifacts:             stats.Artifacts,
		ArtifactsByKind:       cloneIntMap(stats.ArtifactsByKind),
		ArtifactBytes:         stats.ArtifactBytes,
		ArtifactBytesByKind:   cloneInt64Map(stats.ArtifactBytesByKind),
		TotalRuns:             sumIntMap(stats.Runs),
		Runs:                  cloneIntMap(stats.Runs),
		RunDuration:           runDurationStatsDTOFromSession(stats.RunDuration),
		RunDurationByProvider: runDurationStatsDTOMapFromSession(stats.RunDurationByProvider),
		RunDurationByModel:    runDurationStatsDTOMapFromSession(stats.RunDurationByModel),
		RunUsage:              usageDTOFromLLM(stats.RunUsage),
		RunUsageByProvider:    usageDTOMapFromLLM(stats.RunUsageByProvider),
		RunUsageByModel:       usageDTOMapFromLLM(stats.RunUsageByModel),
		TotalResources:        sumIntMap(stats.Resources),
		Resources:             cloneIntMap(stats.Resources),
		ResourcesByEnabled:    cloneIntMap(stats.ResourcesByEnabled),
	}
}

func runDurationStatsDTOFromSession(stats session.RunDurationStats) RunDurationStatsDTO {
	return RunDurationStatsDTO{Count: stats.Count, SumMS: stats.SumMS, AvgMS: stats.AvgMS, MaxMS: stats.MaxMS, P95MS: stats.P95MS}
}

func runDurationStatsDTOMapFromSession(in map[string]session.RunDurationStats) map[string]RunDurationStatsDTO {
	if in == nil {
		return nil
	}
	out := make(map[string]RunDurationStatsDTO, len(in))
	for key, stats := range in {
		out[key] = runDurationStatsDTOFromSession(stats)
	}
	return out
}

func usageDTOFromLLM(usage llm.Usage) UsageDTO {
	return UsageDTO{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, ReasoningTokens: usage.ReasoningTokens}
}

func usageDTOMapFromLLM(in map[string]llm.Usage) map[string]UsageDTO {
	if in == nil {
		return nil
	}
	out := make(map[string]UsageDTO, len(in))
	for key, usage := range in {
		out[key] = usageDTOFromLLM(usage)
	}
	return out
}

func sumIntMap(in map[string]int) int {
	total := 0
	for _, value := range in {
		total += value
	}
	return total
}

func cloneIntMap(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInt64Map(in map[string]int64) map[string]int64 {
	if in == nil {
		return nil
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
