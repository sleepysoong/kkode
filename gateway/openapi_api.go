package gateway

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var embeddedOpenAPIYAML []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeMethodNotAllowed(w, r, "지원하지 않는 openapi 요청이에요", http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(embeddedOpenAPIYAML)
}
