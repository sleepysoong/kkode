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

// IdempotencyKeyHeader는 외부 adapter가 같은 run 생성 요청의 재시도를 묶을 때 쓰는 header예요.
const IdempotencyKeyHeader = "Idempotency-Key"

// RequestIDMetadataKey는 HTTP 요청과 background run/event를 연결하는 metadata key예요.
const RequestIDMetadataKey = "request_id"

// IdempotencyMetadataKey는 같은 run 생성 요청을 중복 접수하지 않도록 저장하는 metadata key예요.
const IdempotencyMetadataKey = "idempotency_key"

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

func withRequestIDMetadata(metadata map[string]string, requestID string) map[string]string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return cloneMap(metadata)
	}
	out := cloneMap(metadata)
	if out == nil {
		out = map[string]string{}
	}
	out[RequestIDMetadataKey] = requestID
	return out
}

func withIdempotencyMetadata(metadata map[string]string, idempotencyKey string) map[string]string {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return cloneMap(metadata)
	}
	out := cloneMap(metadata)
	if out == nil {
		out = map[string]string{}
	}
	out[IdempotencyMetadataKey] = idempotencyKey
	return out
}
