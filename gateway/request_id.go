package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RequestIDHeader는 외부 adapter와 gateway 로그가 같은 요청을 찾을 때 쓰는 공통 header예요.
const RequestIDHeader = "X-Request-Id"

type requestIDContextKey struct{}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return "req_" + hex.EncodeToString(buf[:])
	}
	return "req_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func withRequestID(r *http.Request, id string) *http.Request {
	id = strings.TrimSpace(id)
	if id == "" {
		return r
	}
	ctx := context.WithValue(r.Context(), requestIDContextKey{}, id)
	return r.WithContext(ctx)
}

func requestIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id, ok := r.Context().Value(requestIDContextKey{}).(string); ok && id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get(RequestIDHeader))
}
