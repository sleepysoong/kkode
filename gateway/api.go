package gateway

import (
	"net/http"
	"strings"
)

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	path = strings.Trim(path, "/")
	parts := splitPath(path)
	if len(parts) == 0 {
		s.handleAPIIndex(w, r, parts)
		return
	}
	switch parts[0] {
	case "openapi.yaml":
		s.handleOpenAPI(w, r, parts)
	case "providers":
		s.handleProviders(w, r, parts)
	case "models":
		s.handleModels(w, r, parts)
	case "sessions":
		s.handleSessions(w, r, parts)
	case "runs":
		s.handleRuns(w, r, parts)
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "API endpoint를 찾을 수 없어요")
	}
}

func (s *Server) handleAPIIndex(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 0 || r.Method != http.MethodGet {
		writeMethodNotAllowed(w, r, "지원하지 않는 API index 요청이에요", http.MethodGet)
		return
	}
	writeJSON(w, APIIndexResponse{Version: s.cfg.Version, Commit: s.cfg.Commit, Links: APIIndexLinks(), Operations: APIIndexOperations()})
}
