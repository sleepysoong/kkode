package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorDTO는 모든 gateway 실패 응답의 표준 오류 본문이에요.
// 외부 SDK와 웹/Discord adapter는 code/request_id를 기준으로 재시도와 사용자 메시지를 결정해요.
type ErrorDTO struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope는 gateway의 표준 error envelope이에요.
type ErrorEnvelope struct {
	Error ErrorDTO `json:"error"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := requestIDFromRequest(r)
	writeJSONStatus(w, status, ErrorEnvelope{Error: ErrorDTO{Code: code, Message: message, RequestID: requestID}})
}

func writeJSONDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "요청 body가 gateway 제한보다 커요")
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
}

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
