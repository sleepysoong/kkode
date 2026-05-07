package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

type CheckpointDTO struct {
	ID        string          `json:"id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	CreatedAt time.Time       `json:"created_at,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type CheckpointListResponse struct {
	Checkpoints     []CheckpointDTO `json:"checkpoints"`
	Limit           int             `json:"limit,omitempty"`
	ResultTruncated bool            `json:"result_truncated,omitempty"`
}

func (s *Server) handleSessionCheckpoints(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	store := s.checkpointStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "checkpoint_store_missing", "이 gateway에는 checkpoint store가 연결되지 않았어요")
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.listSessionCheckpoints(w, r, store, sessionID)
		case http.MethodPost:
			s.createSessionCheckpoint(w, r, store, sessionID)
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 checkpoint method예요")
		}
		return
	}
	if len(rest) == 1 && r.Method == http.MethodGet {
		s.getSessionCheckpoint(w, r, store, sessionID, rest[0])
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "checkpoint endpoint를 찾을 수 없어요")
}

func (s *Server) listSessionCheckpoints(w http.ResponseWriter, r *http.Request, store session.CheckpointStore, sessionID string) {
	limit := queryLimit(r, "limit", 50, 200)
	items, err := store.ListCheckpoints(r.Context(), session.CheckpointQuery{SessionID: sessionID, Limit: limit + 1})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_checkpoints_failed", err.Error())
		return
	}
	items, truncated := trimCheckpoints(items, limit)
	out := make([]CheckpointDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toCheckpointDTO(item))
	}
	writeJSON(w, CheckpointListResponse{Checkpoints: out, Limit: limit, ResultTruncated: truncated})
}

func (s *Server) createSessionCheckpoint(w http.ResponseWriter, r *http.Request, store session.CheckpointStore, sessionID string) {
	var req CheckpointDTO
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	cp := session.Checkpoint{ID: strings.TrimSpace(req.ID), SessionID: sessionID, TurnID: strings.TrimSpace(req.TurnID), CreatedAt: req.CreatedAt, Payload: req.Payload}
	if cp.ID == "" {
		cp.ID = session.NewID("cp")
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = s.cfg.Now()
	}
	if len(cp.Payload) == 0 {
		cp.Payload = json.RawMessage(`{}`)
	}
	if err := store.SaveCheckpoint(r.Context(), cp); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_checkpoint_failed", err.Error())
		return
	}
	loaded, err := store.LoadCheckpoint(r.Context(), sessionID, cp.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "load_checkpoint_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, toCheckpointDTO(loaded))
}

func (s *Server) getSessionCheckpoint(w http.ResponseWriter, r *http.Request, store session.CheckpointStore, sessionID string, checkpointID string) {
	cp, err := store.LoadCheckpoint(r.Context(), sessionID, checkpointID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "checkpoint_not_found", err.Error())
		return
	}
	writeJSON(w, toCheckpointDTO(cp))
}

func (s *Server) checkpointStore() session.CheckpointStore {
	store, _ := s.cfg.Store.(session.CheckpointStore)
	return store
}

func toCheckpointDTO(cp session.Checkpoint) CheckpointDTO {
	return CheckpointDTO{ID: cp.ID, SessionID: cp.SessionID, TurnID: cp.TurnID, CreatedAt: cp.CreatedAt, Payload: cp.Payload}
}
