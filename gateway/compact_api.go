package gateway

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sleepysoong/kkode/session"
)

// SessionCompactRequest는 외부 adapter가 오래된 turn을 요약할 때 쓰는 요청이에요.
type SessionCompactRequest struct {
	PreserveFirstNTurns int  `json:"preserve_first_n_turns,omitempty"`
	PreserveLastNTurns  int  `json:"preserve_last_n_turns,omitempty"`
	Checkpoint          bool `json:"checkpoint,omitempty"`
}

// SessionCompactResponse는 compaction 결과와 갱신된 session 요약을 반환해요.
type SessionCompactResponse struct {
	Session             SessionDTO     `json:"session"`
	Summary             string         `json:"summary"`
	TotalTurns          int            `json:"total_turns,omitempty"`
	CompactedTurns      int            `json:"compacted_turns,omitempty"`
	PreservedTurns      int            `json:"preserved_turns,omitempty"`
	PreserveFirstNTurns int            `json:"preserve_first_n_turns,omitempty"`
	PreserveLastNTurns  int            `json:"preserve_last_n_turns,omitempty"`
	SummaryBytes        int            `json:"summary_bytes,omitempty"`
	CheckpointCreated   bool           `json:"checkpoint_created,omitempty"`
	Checkpoint          *CheckpointDTO `json:"checkpoint,omitempty"`
}

func (s *Server) compactSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 compact method예요")
		return
	}
	var req SessionCompactRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeJSONDecodeError(w, r, err)
			return
		}
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	if req.PreserveFirstNTurns < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_session_compact", "preserve_first_n_turns는 0 이상이어야 해요")
		return
	}
	if req.PreserveLastNTurns < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_session_compact", "preserve_last_n_turns는 0 이상이어야 해요")
		return
	}
	preserveFirst := req.PreserveFirstNTurns
	preserveLast := req.PreserveLastNTurns
	if preserveLast <= 0 {
		preserveLast = 4
	}
	totalTurns := len(sess.Turns)
	compactedTurns := compactedTurnCount(totalTurns, preserveFirst, preserveLast)
	summary := session.BuildExtractiveSummary(sess, preserveFirst, preserveLast)
	if summary != "" {
		sess.Summary = summary
		sess.Touch()
		if err := s.saveCompactedSession(r.Context(), sess); err != nil {
			writeError(w, r, http.StatusInternalServerError, "compact_session_failed", err.Error())
			return
		}
	}
	resp := SessionCompactResponse{
		Session:             toSessionDTO(sess),
		Summary:             sess.Summary,
		TotalTurns:          totalTurns,
		CompactedTurns:      compactedTurns,
		PreservedTurns:      totalTurns - compactedTurns,
		PreserveFirstNTurns: preserveFirst,
		PreserveLastNTurns:  preserveLast,
		SummaryBytes:        len(sess.Summary),
	}
	if req.Checkpoint {
		checkpoint, ok := s.saveCompactionCheckpoint(w, r, sess, summary, preserveFirst, preserveLast)
		if !ok {
			return
		}
		resp.CheckpointCreated = true
		resp.Checkpoint = checkpoint
	}
	writeJSON(w, resp)
}

func (s *Server) saveCompactedSession(ctx context.Context, sess *session.Session) error {
	if store, ok := s.cfg.Store.(session.IncrementalStore); ok {
		return store.SaveSessionState(ctx, sess)
	}
	return s.cfg.Store.SaveSession(ctx, sess)
}

func compactedTurnCount(total int, preserveFirst int, preserveLast int) int {
	if total <= 0 {
		return 0
	}
	compacted := 0
	for i := 0; i < total; i++ {
		preserved := (preserveFirst > 0 && i < preserveFirst) || (preserveLast > 0 && i >= total-preserveLast)
		if !preserved {
			compacted++
		}
	}
	return compacted
}

func (s *Server) saveCompactionCheckpoint(w http.ResponseWriter, r *http.Request, sess *session.Session, summary string, preserveFirst int, preserveLast int) (*CheckpointDTO, bool) {
	store := s.checkpointStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "checkpoint_store_missing", "이 gateway에는 checkpoint store가 연결되지 않았어요")
		return nil, false
	}
	payload, _ := json.Marshal(map[string]any{"kind": "session.compaction", "summary": summary, "preserve_first_n_turns": preserveFirst, "preserve_last_n_turns": preserveLast})
	cp := session.Checkpoint{ID: session.NewID("cp"), SessionID: sess.ID, CreatedAt: s.cfg.Now(), Payload: payload}
	if len(sess.Turns) > 0 {
		cp.TurnID = sess.Turns[len(sess.Turns)-1].ID
	}
	if err := store.SaveCheckpoint(r.Context(), cp); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_checkpoint_failed", err.Error())
		return nil, false
	}
	loaded, err := store.LoadCheckpoint(r.Context(), sess.ID, cp.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "load_checkpoint_failed", err.Error())
		return nil, false
	}
	dto := toCheckpointDTO(loaded)
	return &dto, true
}
