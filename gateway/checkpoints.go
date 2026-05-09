package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

const maxCheckpointIDBytes = 128
const maxCheckpointPayloadBytes = 1 << 20

type CheckpointDTO struct {
	ID        string          `json:"id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	CreatedAt time.Time       `json:"created_at,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type CheckpointListResponse struct {
	Checkpoints      []CheckpointDTO `json:"checkpoints"`
	TotalCheckpoints int             `json:"total_checkpoints,omitempty"`
	Limit            int             `json:"limit,omitempty"`
	Offset           int             `json:"offset,omitempty"`
	NextOffset       int             `json:"next_offset,omitempty"`
	ResultTruncated  bool            `json:"result_truncated,omitempty"`
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
			writeMethodNotAllowed(w, r, "지원하지 않는 checkpoint method예요", http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(rest) == 1 && r.Method == http.MethodGet {
		s.getSessionCheckpoint(w, r, store, sessionID, rest[0])
		return
	}
	if len(rest) == 1 {
		writeMethodNotAllowed(w, r, "지원하지 않는 checkpoint method예요", http.MethodGet)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "checkpoint endpoint를 찾을 수 없어요")
}

func (s *Server) listSessionCheckpoints(w http.ResponseWriter, r *http.Request, store session.CheckpointStore, sessionID string) {
	limit, ok := queryLimitParam(w, r, "limit", 50, 200, "invalid_checkpoint_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_checkpoint_list")
	if !ok {
		return
	}
	turnID := strings.TrimSpace(r.URL.Query().Get("turn_id"))
	if err := validateOptionalIDFilter("turn_id", turnID, maxRunIDBytes); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_checkpoint_list", err.Error())
		return
	}
	query := session.CheckpointQuery{SessionID: sessionID, TurnID: turnID}
	totalCheckpoints := 0
	if counter, ok := store.(session.CheckpointCounter); ok {
		total, err := counter.CountCheckpoints(r.Context(), query)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "count_checkpoints_failed", err.Error())
			return
		}
		totalCheckpoints = total
	}
	pageQuery := query
	pageQuery.Limit = limit + 1
	pageQuery.Offset = offset
	items, err := store.ListCheckpoints(r.Context(), pageQuery)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_checkpoints_failed", err.Error())
		return
	}
	items, returned, truncated := trimCheckpoints(items, limit)
	out := make([]CheckpointDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toCheckpointDTO(item))
	}
	writeJSON(w, CheckpointListResponse{Checkpoints: out, TotalCheckpoints: totalCheckpoints, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) createSessionCheckpoint(w http.ResponseWriter, r *http.Request, store session.CheckpointStore, sessionID string) {
	var req CheckpointDTO
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.TurnID = strings.TrimSpace(req.TurnID)
	if err := validateCheckpointDTO(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_checkpoint", err.Error())
		return
	}
	if ok := s.validateCheckpointTarget(w, r, sessionID, req.TurnID); !ok {
		return
	}
	cp := session.Checkpoint{ID: req.ID, SessionID: sessionID, TurnID: req.TurnID, CreatedAt: req.CreatedAt, Payload: req.Payload}
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

func (s *Server) validateCheckpointTarget(w http.ResponseWriter, r *http.Request, sessionID string, turnID string) bool {
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return false
	}
	if turnID == "" {
		return true
	}
	for _, turn := range sess.Turns {
		if turn.ID == turnID {
			return true
		}
	}
	writeError(w, r, http.StatusBadRequest, "invalid_checkpoint", "turn_id가 session에 없어요")
	return false
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

func validateCheckpointDTO(dto CheckpointDTO) error {
	if dto.ID != "" {
		if len(dto.ID) > maxCheckpointIDBytes {
			return fmt.Errorf("checkpoint id는 %d byte 이하여야 해요", maxCheckpointIDBytes)
		}
		if !validRunMetadataKey(dto.ID) {
			return fmt.Errorf("checkpoint id는 영문/숫자/._- 문자만 쓸 수 있어요")
		}
	}
	if len(dto.Payload) > maxCheckpointPayloadBytes {
		return fmt.Errorf("checkpoint payload는 %d byte 이하여야 해요", maxCheckpointPayloadBytes)
	}
	return nil
}
