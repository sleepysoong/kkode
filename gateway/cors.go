package gateway

import (
	"net/http"
	"strings"
)

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	origins := s.cfg.CORSOrigins
	if len(origins) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedCORSOrigin(origins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+RequestIDHeader+", "+IdempotencyKeyHeader)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", RequestIDHeader+", "+IdempotencyReplayHeader)
		}
		if r.Method == http.MethodOptions && origin != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedCORSOrigin(allowed []string, origin string) bool {
	if origin == "" {
		return false
	}
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == "*" || item == origin {
			return true
		}
	}
	return false
}
