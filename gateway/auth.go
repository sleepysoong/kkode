package gateway

import (
	"net"
	"net/http"
	"strings"
)

func (s *Server) withAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.AllowLocalhostNoAuth && isLocalRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if bearerToken(r.Header.Get("Authorization")) != s.cfg.APIKey {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "API key가 필요해요")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}
