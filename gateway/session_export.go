package gateway

import (
	"net/http"
	"time"

	"github.com/sleepysoong/kkode/session"
)

const sessionExportFormatVersion = "kkode.session.export.v1"

// SessionExportResponse는 session 복구/이관/debug를 위해 관련 상태를 한 번에 묶은 JSON이에요.
type SessionExportResponse struct {
	FormatVersion string                 `json:"format_version"`
	ExportedAt    time.Time              `json:"exported_at"`
	Session       SessionDTO             `json:"session"`
	RawSession    session.Session        `json:"raw_session"`
	Counts        SessionExportCountsDTO `json:"counts"`
	Turns         []TurnDTO              `json:"turns"`
	Events        []EventDTO             `json:"events"`
	Todos         []TodoDTO              `json:"todos"`
	Checkpoints   []CheckpointDTO        `json:"checkpoints,omitempty"`
	Runs          []RunDTO               `json:"runs,omitempty"`
}

// SessionExportCountsDTO는 export bundle이 잘렸는지 adapter가 빠르게 검증할 때 쓰는 카운트예요.
type SessionExportCountsDTO struct {
	Turns       int `json:"turns"`
	Events      int `json:"events"`
	Todos       int `json:"todos"`
	Checkpoints int `json:"checkpoints"`
	Runs        int `json:"runs"`
}

func (s *Server) exportSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 session export method예요")
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	resp := SessionExportResponse{
		FormatVersion: sessionExportFormatVersion,
		ExportedAt:    s.cfg.Now(),
		Session:       toSessionDTO(sess),
		RawSession:    *sess,
		Turns:         exportTurnDTOs(sess),
		Events:        exportEventDTOs(sess),
		Todos:         todoDTOs(sess.Todos),
	}
	if store := s.checkpointStore(); store != nil {
		checkpoints, err := store.ListCheckpoints(r.Context(), session.CheckpointQuery{SessionID: sessionID, Limit: queryLimit(r, "checkpoint_limit", 200, 5000)})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "export_checkpoints_failed", err.Error())
			return
		}
		resp.Checkpoints = checkpointDTOs(checkpoints)
	}
	if s.cfg.RunLister != nil {
		runs, err := s.cfg.RunLister(r.Context(), RunQuery{SessionID: sessionID, Limit: queryLimit(r, "run_limit", 200, 5000)})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "export_runs_failed", err.Error())
			return
		}
		resp.Runs = runs
	}
	resp.Counts = SessionExportCountsDTO{Turns: len(resp.Turns), Events: len(resp.Events), Todos: len(resp.Todos), Checkpoints: len(resp.Checkpoints), Runs: len(resp.Runs)}
	writeJSON(w, resp)
}

func exportTurnDTOs(sess *session.Session) []TurnDTO {
	out := make([]TurnDTO, 0, len(sess.Turns))
	for i, turn := range sess.Turns {
		out = append(out, toTurnDTO(sess.ID, i+1, turn))
	}
	return out
}

func exportEventDTOs(sess *session.Session) []EventDTO {
	out := make([]EventDTO, 0, len(sess.Events))
	for i, event := range sess.Events {
		out = append(out, toEventDTO(i+1, event))
	}
	return out
}

func checkpointDTOs(checkpoints []session.Checkpoint) []CheckpointDTO {
	out := make([]CheckpointDTO, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		out = append(out, toCheckpointDTO(checkpoint))
	}
	return out
}
