package gateway

import (
	"net/http"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// StatsResponse는 외부 dashboard adapter가 gateway 저장소 상태를 한 번에 그릴 때 쓰는 응답이에요.
type StatsResponse struct {
	Sessions       int            `json:"sessions"`
	Turns          int            `json:"turns"`
	Events         int            `json:"events"`
	Todos          int            `json:"todos"`
	Checkpoints    int            `json:"checkpoints"`
	Artifacts      int            `json:"artifacts"`
	TotalRuns      int            `json:"total_runs"`
	Runs           map[string]int `json:"runs"`
	RunUsage       UsageDTO       `json:"run_usage"`
	TotalResources int            `json:"total_resources"`
	Resources      map[string]int `json:"resources"`
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
		Sessions:       stats.Sessions,
		Turns:          stats.Turns,
		Events:         stats.Events,
		Todos:          stats.Todos,
		Checkpoints:    stats.Checkpoints,
		Artifacts:      stats.Artifacts,
		TotalRuns:      sumIntMap(stats.Runs),
		Runs:           cloneIntMap(stats.Runs),
		RunUsage:       usageDTOFromLLM(stats.RunUsage),
		TotalResources: sumIntMap(stats.Resources),
		Resources:      cloneIntMap(stats.Resources),
	}
}

func usageDTOFromLLM(usage llm.Usage) UsageDTO {
	return UsageDTO{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, ReasoningTokens: usage.ReasoningTokens}
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
