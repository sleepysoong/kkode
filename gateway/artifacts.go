package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

const (
	maxArtifactIDBytes       = 128
	maxArtifactKindBytes     = 64
	maxArtifactNameBytes     = 256
	maxArtifactMimeTypeBytes = 128
	maxArtifactContentBytes  = 8 << 20
)

type ArtifactDTO struct {
	ID               string            `json:"id,omitempty"`
	SessionID        string            `json:"session_id,omitempty"`
	RunID            string            `json:"run_id,omitempty"`
	TurnID           string            `json:"turn_id,omitempty"`
	Kind             string            `json:"kind"`
	Name             string            `json:"name,omitempty"`
	MimeType         string            `json:"mime_type,omitempty"`
	Content          json.RawMessage   `json:"content,omitempty"`
	ContentBytes     int               `json:"content_bytes,omitempty"`
	ContentTruncated bool              `json:"content_truncated,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	CreatedAt        time.Time         `json:"created_at,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at,omitempty"`
}

type ArtifactListResponse struct {
	Artifacts       []ArtifactDTO `json:"artifacts"`
	Limit           int           `json:"limit,omitempty"`
	Offset          int           `json:"offset,omitempty"`
	NextOffset      int           `json:"next_offset,omitempty"`
	ResultTruncated bool          `json:"result_truncated,omitempty"`
}

type ArtifactPruneRequest struct {
	KeepLatest int `json:"keep_latest"`
}

type ArtifactPruneResponse struct {
	SessionID        string `json:"session_id"`
	KeepLatest       int    `json:"keep_latest"`
	DeletedArtifacts int    `json:"deleted_artifacts"`
}

func (s *Server) handleSessionArtifacts(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	store := s.artifactStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "artifact_store_missing", "이 gateway에는 artifact store가 연결되지 않았어요")
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.listSessionArtifacts(w, r, store, sessionID)
		case http.MethodPost:
			s.createSessionArtifact(w, r, store, sessionID)
		default:
			writeMethodNotAllowed(w, r, "지원하지 않는 artifact method예요", http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(rest) == 1 && rest[0] == "prune" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, r, "지원하지 않는 artifact prune method예요", http.MethodPost)
			return
		}
		s.pruneSessionArtifacts(w, r, sessionID)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "artifact endpoint를 찾을 수 없어요")
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request, parts []string) {
	store := s.artifactStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "artifact_store_missing", "이 gateway에는 artifact store가 연결되지 않았어요")
		return
	}
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			s.getArtifact(w, r, store, parts[1])
		case http.MethodDelete:
			s.deleteArtifact(w, r, store, parts[1])
		default:
			writeMethodNotAllowed(w, r, "지원하지 않는 artifact method예요", http.MethodGet, http.MethodDelete)
		}
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "artifact endpoint를 찾을 수 없어요")
}

func (s *Server) listSessionArtifacts(w http.ResponseWriter, r *http.Request, store session.ArtifactStore, sessionID string) {
	limit, ok := queryLimitParam(w, r, "limit", 50, 200, "invalid_artifact_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_artifact_list")
	if !ok {
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	turnID := strings.TrimSpace(r.URL.Query().Get("turn_id"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if err := validateArtifactFilters(runID, turnID, kind); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_artifact_list", err.Error())
		return
	}
	items, err := store.ListArtifacts(r.Context(), session.ArtifactQuery{SessionID: sessionID, RunID: runID, TurnID: turnID, Kind: kind, Limit: limit + 1, Offset: offset})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_artifacts_failed", err.Error())
		return
	}
	items, returned, truncated := trimArtifacts(items, limit)
	out := make([]ArtifactDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toArtifactDTO(item, -1))
	}
	writeJSON(w, ArtifactListResponse{Artifacts: out, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) createSessionArtifact(w http.ResponseWriter, r *http.Request, store session.ArtifactStore, sessionID string) {
	var req ArtifactDTO
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.RunID = strings.TrimSpace(req.RunID)
	req.TurnID = strings.TrimSpace(req.TurnID)
	req.Kind = strings.TrimSpace(req.Kind)
	req.Name = strings.TrimSpace(req.Name)
	req.MimeType = strings.TrimSpace(req.MimeType)
	if err := validateArtifactDTO(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_artifact", err.Error())
		return
	}
	if ok := s.validateArtifactTarget(w, r, sessionID, req.TurnID); !ok {
		return
	}
	artifact := artifactFromDTO(req, sessionID)
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = s.cfg.Now()
	}
	saved, err := store.SaveArtifact(r.Context(), artifact)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_artifact_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, toArtifactDTO(saved, 0))
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request, store session.ArtifactStore, id string) {
	maxBytes, ok := queryNonNegativeIntParam(w, r, "max_content_bytes", maxArtifactContentBytes, "invalid_artifact")
	if !ok {
		return
	}
	if maxBytes > maxArtifactContentBytes {
		writeError(w, r, http.StatusBadRequest, "invalid_artifact", fmt.Sprintf("max_content_bytes는 %d 이하여야 해요", maxArtifactContentBytes))
		return
	}
	artifact, err := store.LoadArtifact(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "artifact_not_found", err.Error())
		return
	}
	writeJSON(w, toArtifactDTO(artifact, maxBytes))
}

func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request, store session.ArtifactStore, id string) {
	if err := store.DeleteArtifact(r.Context(), id); err != nil {
		writeError(w, r, http.StatusNotFound, "artifact_not_found", err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true, "artifact_id": id})
}

func (s *Server) pruneSessionArtifacts(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req ArtifactPruneRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	if req.KeepLatest < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_artifact_prune", "keep_latest는 0 이상이어야 해요")
		return
	}
	if _, err := s.cfg.Store.LoadSession(r.Context(), sessionID); err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	store, ok := s.cfg.Store.(session.ArtifactPruneStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "artifact_prune_store_missing", "이 gateway에는 artifact prune store가 연결되지 않았어요")
		return
	}
	deleted, err := store.PruneArtifacts(r.Context(), sessionID, req.KeepLatest)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "prune_artifacts_failed", err.Error())
		return
	}
	writeJSON(w, ArtifactPruneResponse{SessionID: sessionID, KeepLatest: req.KeepLatest, DeletedArtifacts: deleted})
}

func (s *Server) artifactStore() session.ArtifactStore {
	store, _ := s.cfg.Store.(session.ArtifactStore)
	return store
}

func (s *Server) validateArtifactTarget(w http.ResponseWriter, r *http.Request, sessionID string, turnID string) bool {
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
	writeError(w, r, http.StatusBadRequest, "invalid_artifact", "artifact turn_id가 session에 없어요")
	return false
}

func validateArtifactDTO(dto ArtifactDTO) error {
	if dto.ID != "" {
		if len(dto.ID) > maxArtifactIDBytes {
			return fmt.Errorf("artifact id는 %d byte 이하여야 해요", maxArtifactIDBytes)
		}
		if !validRunMetadataKey(dto.ID) {
			return fmt.Errorf("artifact id는 영문/숫자/._- 문자만 쓸 수 있어요")
		}
	}
	if dto.RunID != "" && !validRunMetadataKey(dto.RunID) {
		return fmt.Errorf("artifact run_id는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	if dto.TurnID != "" && !validRunMetadataKey(dto.TurnID) {
		return fmt.Errorf("artifact turn_id는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	if dto.Kind == "" {
		return fmt.Errorf("artifact kind가 필요해요")
	}
	if len(dto.Kind) > maxArtifactKindBytes {
		return fmt.Errorf("artifact kind는 %d byte 이하여야 해요", maxArtifactKindBytes)
	}
	if !validRunMetadataKey(dto.Kind) {
		return fmt.Errorf("artifact kind는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	if len(dto.Name) > maxArtifactNameBytes {
		return fmt.Errorf("artifact name은 %d byte 이하여야 해요", maxArtifactNameBytes)
	}
	if len(dto.MimeType) > maxArtifactMimeTypeBytes {
		return fmt.Errorf("artifact mime_type은 %d byte 이하여야 해요", maxArtifactMimeTypeBytes)
	}
	if len(dto.Content) > maxArtifactContentBytes {
		return fmt.Errorf("artifact content는 %d byte 이하여야 해요", maxArtifactContentBytes)
	}
	if len(dto.Content) > 0 && !json.Valid(dto.Content) {
		return fmt.Errorf("artifact content는 JSON 값이어야 해요")
	}
	return validateRunMetadata(dto.Metadata)
}

func validateArtifactFilters(runID string, turnID string, kind string) error {
	if runID != "" && !validRunMetadataKey(runID) {
		return fmt.Errorf("run_id는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	if turnID != "" && !validRunMetadataKey(turnID) {
		return fmt.Errorf("turn_id는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	if kind != "" && !validRunMetadataKey(kind) {
		return fmt.Errorf("kind는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	return nil
}

func artifactFromDTO(dto ArtifactDTO, sessionID string) session.Artifact {
	return session.Artifact{
		ID:        dto.ID,
		SessionID: sessionID,
		RunID:     dto.RunID,
		TurnID:    dto.TurnID,
		Kind:      dto.Kind,
		Name:      dto.Name,
		MimeType:  dto.MimeType,
		Content:   cloneRawMessage(dto.Content),
		Metadata:  cloneMap(dto.Metadata),
		CreatedAt: dto.CreatedAt,
	}
}

func toArtifactDTO(artifact session.Artifact, maxContentBytes int) ArtifactDTO {
	content := cloneRawMessage(artifact.Content)
	contentBytes := len(content)
	truncated := false
	if maxContentBytes < 0 {
		content = nil
	}
	if maxContentBytes > 0 && contentBytes > maxContentBytes {
		content = json.RawMessage(`{"truncated":true}`)
		truncated = true
	}
	return ArtifactDTO{
		ID:               artifact.ID,
		SessionID:        artifact.SessionID,
		RunID:            artifact.RunID,
		TurnID:           artifact.TurnID,
		Kind:             artifact.Kind,
		Name:             artifact.Name,
		MimeType:         artifact.MimeType,
		Content:          content,
		ContentBytes:     contentBytes,
		ContentTruncated: truncated,
		Metadata:         cloneMap(artifact.Metadata),
		CreatedAt:        artifact.CreatedAt,
		UpdatedAt:        artifact.UpdatedAt,
	}
}

func trimArtifacts(artifacts []session.Artifact, limit int) ([]session.Artifact, int, bool) {
	truncated := len(artifacts) > limit
	if truncated {
		artifacts = artifacts[:limit]
	}
	return artifacts, len(artifacts), truncated
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}
