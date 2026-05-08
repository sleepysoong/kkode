package gateway

import (
	"net/http"
	"time"
)

// AccessLogEntry는 gateway 요청 하나를 외부 logger/metric으로 넘기기 위한 공통 구조예요.
type AccessLogEntry struct {
	RequestID string
	Method    string
	Path      string
	Status    int
	Bytes     int64
	Duration  time.Duration
	Remote    string
	UserAgent string
}

// AccessLogger는 Discord/web adapter나 production logger가 gateway access log를 받는 hook이에요.
type AccessLogger func(AccessLogEntry)

type responseStatsWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseStatsWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseStatsWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *responseStatsWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	if s.cfg.AccessLogger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := s.cfg.Now()
		stats := &responseStatsWriter{ResponseWriter: w}
		next.ServeHTTP(stats, r)
		status := stats.status
		if status == 0 {
			status = http.StatusOK
		}
		safeAccessLog(s.cfg.AccessLogger, AccessLogEntry{
			RequestID: requestIDFromRequest(r),
			Method:    r.Method,
			Path:      r.URL.RequestURI(),
			Status:    status,
			Bytes:     stats.bytes,
			Duration:  s.cfg.Now().Sub(started),
			Remote:    r.RemoteAddr,
			UserAgent: r.UserAgent(),
		})
	})
}

func safeAccessLog(logger AccessLogger, entry AccessLogEntry) {
	if logger == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	logger(entry)
}
