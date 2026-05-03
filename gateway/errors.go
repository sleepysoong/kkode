package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
)

type apiError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := requestIDFromRequest(r)
	writeJSONStatus(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, RequestID: requestID}})
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
