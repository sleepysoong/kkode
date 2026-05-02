package gateway

import (
	"net/http"

	"github.com/sleepysoong/kkode/session"
)

// StatsResponse는 외부 dashboard adapter가 gateway 저장소 상태를 한 번에 그릴 때 쓰는 응답이에요.
type StatsResponse struct {
	Sessions    int            `json:"sessions"`
	Turns       int            `json:"turns"`
	Events      int            `json:"events"`
	Todos       int            `json:"todos"`
	Checkpoints int            `json:"checkpoints"`
	Runs        map[string]int `json:"runs"`
	Resources   map[string]int `json:"resources"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 stats 요청이에요")
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
		Sessions:    stats.Sessions,
		Turns:       stats.Turns,
		Events:      stats.Events,
		Todos:       stats.Todos,
		Checkpoints: stats.Checkpoints,
		Runs:        cloneIntMap(stats.Runs),
		Resources:   cloneIntMap(stats.Resources),
	}
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
