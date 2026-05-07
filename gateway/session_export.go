package gateway

import (
	"net/http"
	"strings"
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

// SessionImportRequest는 export bundle을 다른 gateway/store로 다시 심을 때 쓰는 요청이에요.
type SessionImportRequest struct {
	FormatVersion string          `json:"format_version,omitempty"`
	RawSession    session.Session `json:"raw_session"`
	Checkpoints   []CheckpointDTO `json:"checkpoints,omitempty"`
	Runs          []RunDTO        `json:"runs,omitempty"`
	NewSessionID  string          `json:"new_session_id,omitempty"`
	Overwrite     bool            `json:"overwrite,omitempty"`
}

// SessionImportResponse는 import 결과와 실제 저장된 항목 수를 알려줘요.
type SessionImportResponse struct {
	ImportedAt         time.Time              `json:"imported_at"`
	Session            SessionDTO             `json:"session"`
	Counts             SessionExportCountsDTO `json:"counts"`
	RewrittenSessionID bool                   `json:"rewritten_session_id,omitempty"`
	OriginalSessionID  string                 `json:"original_session_id,omitempty"`
	RequestedOverwrite bool                   `json:"requested_overwrite,omitempty"`
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

func (s *Server) importSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 session import method예요")
		return
	}
	var req SessionImportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	if req.FormatVersion != "" && req.FormatVersion != sessionExportFormatVersion {
		writeError(w, r, http.StatusBadRequest, "unsupported_export_format", "지원하지 않는 session export format이에요")
		return
	}
	imported := req.RawSession
	if strings.TrimSpace(imported.ID) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_import", "raw_session.id가 필요해요")
		return
	}
	originalID := imported.ID
	if newID := strings.TrimSpace(req.NewSessionID); newID != "" {
		rewriteImportedSessionID(&imported, newID)
	}
	if !req.Overwrite {
		if _, err := s.cfg.Store.LoadSession(r.Context(), imported.ID); err == nil {
			writeError(w, r, http.StatusConflict, "session_exists", "같은 id의 session이 이미 있어요")
			return
		} else if !strings.Contains(err.Error(), "not found") {
			writeError(w, r, http.StatusInternalServerError, "check_session_failed", err.Error())
			return
		}
	}
	if err := s.cfg.Store.SaveSession(r.Context(), &imported); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_imported_session_failed", err.Error())
		return
	}
	counts := SessionExportCountsDTO{Turns: len(imported.Turns), Events: len(imported.Events), Todos: len(imported.Todos)}
	if len(req.Checkpoints) > 0 {
		store := s.checkpointStore()
		if store == nil {
			writeError(w, r, http.StatusNotImplemented, "checkpoint_store_missing", "이 gateway에는 checkpoint store가 연결되지 않았어요")
			return
		}
		for _, item := range req.Checkpoints {
			cp := checkpointFromDTO(item, imported.ID)
			if err := store.SaveCheckpoint(r.Context(), cp); err != nil {
				writeError(w, r, http.StatusInternalServerError, "import_checkpoint_failed", err.Error())
				return
			}
			counts.Checkpoints++
		}
	}
	if len(req.Runs) > 0 {
		runStore, ok := s.cfg.Store.(session.RunStore)
		if !ok {
			writeError(w, r, http.StatusNotImplemented, "run_store_missing", "이 gateway에는 RunStore가 연결되지 않았어요")
			return
		}
		for _, item := range req.Runs {
			run := sessionRunFromDTO(item)
			run.SessionID = imported.ID
			if run.EventsURL == "" || originalID != imported.ID {
				run.EventsURL = "/api/v1/sessions/" + imported.ID + "/events"
			}
			if _, err := runStore.SaveRun(r.Context(), run); err != nil {
				writeError(w, r, http.StatusInternalServerError, "import_run_failed", err.Error())
				return
			}
			counts.Runs++
		}
	}
	resp := SessionImportResponse{
		ImportedAt:         s.cfg.Now(),
		Session:            toSessionDTO(&imported),
		Counts:             counts,
		RewrittenSessionID: originalID != imported.ID,
		OriginalSessionID:  originalID,
		RequestedOverwrite: req.Overwrite,
	}
	writeJSONStatus(w, http.StatusCreated, resp)
}

func rewriteImportedSessionID(sess *session.Session, newID string) {
	oldID := sess.ID
	sess.ID = newID
	for i := range sess.Events {
		if sess.Events[i].SessionID == "" || sess.Events[i].SessionID == oldID {
			sess.Events[i].SessionID = newID
		}
	}
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

func checkpointFromDTO(dto CheckpointDTO, sessionID string) session.Checkpoint {
	cp := session.Checkpoint{ID: strings.TrimSpace(dto.ID), SessionID: sessionID, TurnID: strings.TrimSpace(dto.TurnID), CreatedAt: dto.CreatedAt, Payload: dto.Payload}
	if cp.ID == "" {
		cp.ID = session.NewID("cp")
	}
	return cp
}
