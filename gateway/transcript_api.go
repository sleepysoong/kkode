package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// TranscriptResponse는 외부 adapter가 session 대화를 한 번에 렌더링하거나 내보낼 때 쓰는 응답이에요.
type TranscriptResponse struct {
	Session           SessionDTO `json:"session"`
	Turns             []TurnDTO  `json:"turns"`
	Markdown          string     `json:"markdown"`
	MarkdownBytes     int        `json:"markdown_bytes,omitempty"`
	MarkdownTruncated bool       `json:"markdown_truncated,omitempty"`
	Redacted          bool       `json:"redacted"`
}

// RunTranscriptResponse는 run id 하나로 외부 adapter가 결과와 trace를 렌더링할 때 쓰는 응답이에요.
type RunTranscriptResponse struct {
	Run               RunDTO        `json:"run"`
	Session           SessionDTO    `json:"session"`
	Turn              *TurnDTO      `json:"turn,omitempty"`
	Events            []EventDTO    `json:"events,omitempty"`
	RunEvents         []RunEventDTO `json:"run_events,omitempty"`
	Markdown          string        `json:"markdown"`
	MarkdownBytes     int           `json:"markdown_bytes,omitempty"`
	MarkdownTruncated bool          `json:"markdown_truncated,omitempty"`
	Redacted          bool          `json:"redacted"`
}

// RequestCorrelationTranscriptResponse는 외부 request id 하나에서 파생된 run transcript 묶음이에요.
type RequestCorrelationTranscriptResponse struct {
	RequestID         string                  `json:"request_id"`
	Transcripts       []RunTranscriptResponse `json:"transcripts"`
	Markdown          string                  `json:"markdown"`
	MarkdownBytes     int                     `json:"markdown_bytes,omitempty"`
	MarkdownTruncated bool                    `json:"markdown_truncated,omitempty"`
	Redacted          bool                    `json:"redacted"`
}

func (s *Server) getSessionTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 transcript method예요")
		return
	}
	maxMarkdownBytes, ok := transcriptMarkdownLimit(w, r)
	if !ok {
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	redacted := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("redact")), "false")
	resp := toTranscriptResponse(sess, redacted)
	resp.Markdown, resp.MarkdownBytes, resp.MarkdownTruncated = limitTranscriptMarkdown(resp.Markdown, maxMarkdownBytes)
	writeJSON(w, resp)
}

func (s *Server) getRequestTranscript(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 request transcript method예요")
		return
	}
	if s.cfg.RunLister == nil {
		writeError(w, r, http.StatusNotImplemented, "run_lister_missing", "이 gateway에는 RunLister가 연결되지 않았어요")
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request_id", "request_id가 필요해요")
		return
	}
	maxMarkdownBytes, ok := transcriptMarkdownLimit(w, r)
	if !ok {
		return
	}
	runLimit := queryLimit(r, "run_limit", 50, 200)
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{RequestID: requestID, Limit: runLimit})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	redacted := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("redact")), "false")
	sessionCache := make(map[string]*session.Session)
	transcripts := make([]RunTranscriptResponse, 0, len(runs))
	for _, run := range runs {
		sess, ok := sessionCache[run.SessionID]
		if !ok {
			sess, err = s.cfg.Store.LoadSession(r.Context(), run.SessionID)
			if err != nil {
				writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
				return
			}
			sessionCache[run.SessionID] = sess
		}
		transcripts = append(transcripts, s.toRunTranscriptResponse(r, run, sess, redacted, maxMarkdownBytes))
	}
	resp := RequestCorrelationTranscriptResponse{
		RequestID:   requestID,
		Transcripts: transcripts,
		Markdown:    requestCorrelationTranscriptMarkdown(requestID, transcripts),
		Redacted:    redacted,
	}
	if redacted {
		resp.Markdown = llm.RedactSecrets(resp.Markdown)
	}
	resp.Markdown, resp.MarkdownBytes, resp.MarkdownTruncated = limitTranscriptMarkdown(resp.Markdown, maxMarkdownBytes)
	writeJSON(w, resp)
}

func (s *Server) getRunTranscript(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 run transcript method예요")
		return
	}
	if s.cfg.RunGetter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_getter_missing", "이 gateway에는 RunGetter가 연결되지 않았어요")
		return
	}
	maxMarkdownBytes, ok := transcriptMarkdownLimit(w, r)
	if !ok {
		return
	}
	run, err := s.cfg.RunGetter(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), run.SessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	redacted := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("redact")), "false")
	resp := s.toRunTranscriptResponse(r, *run, sess, redacted, maxMarkdownBytes)
	writeJSON(w, resp)
}

func (s *Server) toRunTranscriptResponse(r *http.Request, run RunDTO, sess *session.Session, redacted bool, maxMarkdownBytes int) RunTranscriptResponse {
	turn := s.runTranscriptTurn(r, run)
	events := s.runTranscriptEvents(r, run)
	runEvents := s.runEventSnapshot(r, run.ID, run, 0)
	markdown := runTranscriptMarkdown(run, sess, turn, events, runEvents)
	if redacted {
		markdown = llm.RedactSecrets(markdown)
		sessionDTO := toSessionDTO(sess)
		sessionDTO.ProjectRoot = llm.RedactSecrets(sessionDTO.ProjectRoot)
		sessionDTO.Summary = llm.RedactSecrets(sessionDTO.Summary)
		sessionDTO.Metadata = redactStringMap(sessionDTO.Metadata)
		run.Prompt = llm.RedactSecrets(run.Prompt)
		run.Error = llm.RedactSecrets(run.Error)
		run.Metadata = redactStringMap(run.Metadata)
		if turn != nil {
			turn.Prompt = llm.RedactSecrets(turn.Prompt)
			turn.ResponseText = llm.RedactSecrets(turn.ResponseText)
			turn.Error = llm.RedactSecrets(turn.Error)
			for i := range turn.Messages {
				turn.Messages[i].Content = llm.RedactSecrets(turn.Messages[i].Content)
			}
		}
		for i := range events {
			events[i].Error = llm.RedactSecrets(events[i].Error)
			if len(events[i].Payload) > 0 {
				events[i].Payload = redactRawJSON(events[i].Payload)
			}
		}
		for i := range runEvents {
			runEvents[i].Message = llm.RedactSecrets(runEvents[i].Message)
			runEvents[i].Error = llm.RedactSecrets(runEvents[i].Error)
			runEvents[i].Run.Prompt = llm.RedactSecrets(runEvents[i].Run.Prompt)
			runEvents[i].Run.Error = llm.RedactSecrets(runEvents[i].Run.Error)
			runEvents[i].Run.Metadata = redactStringMap(runEvents[i].Run.Metadata)
			if len(runEvents[i].Payload) > 0 {
				runEvents[i].Payload = redactRawJSON(runEvents[i].Payload)
			}
		}
		markdown, markdownBytes, markdownTruncated := limitTranscriptMarkdown(markdown, maxMarkdownBytes)
		return RunTranscriptResponse{Run: run, Session: sessionDTO, Turn: turn, Events: events, RunEvents: runEvents, Markdown: markdown, MarkdownBytes: markdownBytes, MarkdownTruncated: markdownTruncated, Redacted: redacted}
	}
	markdown, markdownBytes, markdownTruncated := limitTranscriptMarkdown(markdown, maxMarkdownBytes)
	return RunTranscriptResponse{Run: run, Session: toSessionDTO(sess), Turn: turn, Events: events, RunEvents: runEvents, Markdown: markdown, MarkdownBytes: markdownBytes, MarkdownTruncated: markdownTruncated, Redacted: redacted}
}

func transcriptMarkdownLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	return queryNonNegativeLimitParam(w, r, "max_markdown_bytes", 1<<20, 8<<20, "invalid_transcript")
}

func limitTranscriptMarkdown(markdown string, maxMarkdownBytes int) (string, int, bool) {
	return truncateToolOutput(markdown, maxMarkdownBytes)
}

func (s *Server) runTranscriptTurn(r *http.Request, run RunDTO) *TurnDTO {
	if strings.TrimSpace(run.TurnID) == "" {
		return nil
	}
	if timeline, ok := s.cfg.Store.(session.TimelineStore); ok {
		if record, err := timeline.LoadTurn(r.Context(), run.SessionID, run.TurnID); err == nil {
			turn := toTurnDTO(run.SessionID, record.Seq, record.Turn)
			return &turn
		}
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), run.SessionID)
	if err != nil {
		return nil
	}
	for i, turn := range sess.Turns {
		if turn.ID == run.TurnID {
			dto := toTurnDTO(sess.ID, i+1, turn)
			return &dto
		}
	}
	return nil
}

func (s *Server) runTranscriptEvents(r *http.Request, run RunDTO) []EventDTO {
	if strings.TrimSpace(run.TurnID) == "" {
		return nil
	}
	limit := queryLimit(r, "event_limit", 500, 5000)
	out := []EventDTO{}
	if timeline, ok := s.cfg.Store.(session.TimelineStore); ok {
		records, err := timeline.ListEvents(r.Context(), session.EventQuery{SessionID: run.SessionID, Limit: limit})
		if err == nil {
			for _, record := range records {
				if record.Event.TurnID == run.TurnID {
					out = append(out, toEventDTO(record.Seq, record.Event))
				}
			}
			return out
		}
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), run.SessionID)
	if err != nil {
		return nil
	}
	for i, ev := range sess.Events {
		if ev.TurnID != run.TurnID {
			continue
		}
		out = append(out, toEventDTO(i+1, ev))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func toTranscriptResponse(sess *session.Session, redacted bool) TranscriptResponse {
	turns := make([]TurnDTO, 0, len(sess.Turns))
	for i, turn := range sess.Turns {
		turns = append(turns, toTurnDTO(sess.ID, i+1, turn))
	}
	markdown := sessionTranscriptMarkdown(sess, turns)
	if redacted {
		markdown = llm.RedactSecrets(markdown)
		for i := range turns {
			turns[i].Prompt = llm.RedactSecrets(turns[i].Prompt)
			turns[i].ResponseText = llm.RedactSecrets(turns[i].ResponseText)
			turns[i].Error = llm.RedactSecrets(turns[i].Error)
			for j := range turns[i].Messages {
				turns[i].Messages[j].Content = llm.RedactSecrets(turns[i].Messages[j].Content)
			}
		}
	}
	return TranscriptResponse{Session: toSessionDTO(sess), Turns: turns, Markdown: markdown, Redacted: redacted}
}

func runTranscriptMarkdown(run RunDTO, sess *session.Session, turn *TurnDTO, events []EventDTO, runEvents []RunEventDTO) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# kkode run transcript\n\n")
	fmt.Fprintf(&b, "- run: `%s`\n", run.ID)
	fmt.Fprintf(&b, "- status: `%s`\n", run.Status)
	fmt.Fprintf(&b, "- session: `%s`\n", run.SessionID)
	fmt.Fprintf(&b, "- provider: `%s`\n", firstNonEmptyString(run.Provider, sess.ProviderName))
	fmt.Fprintf(&b, "- model: `%s`\n", firstNonEmptyString(run.Model, sess.Model))
	if run.Error != "" {
		fmt.Fprintf(&b, "- error: %s\n", strings.TrimSpace(run.Error))
	}
	fmt.Fprintln(&b)
	if turn != nil {
		fmt.Fprintf(&b, "## Turn `%s`\n\n", turn.ID)
		fmt.Fprintf(&b, "**User**\n\n%s\n\n", strings.TrimSpace(turn.Prompt))
		if strings.TrimSpace(turn.ResponseText) != "" {
			fmt.Fprintf(&b, "**Assistant**\n\n%s\n\n", strings.TrimSpace(turn.ResponseText))
		}
		if turn.Error != "" {
			fmt.Fprintf(&b, "**Error**\n\n%s\n\n", strings.TrimSpace(turn.Error))
		}
	}
	if len(events) > 0 {
		fmt.Fprintln(&b, "## Session events")
		for _, ev := range events {
			fmt.Fprintf(&b, "- `%d` `%s`", ev.Seq, ev.Type)
			if ev.Tool != "" {
				fmt.Fprintf(&b, " tool=`%s`", ev.Tool)
			}
			if ev.Error != "" {
				fmt.Fprintf(&b, " error=%s", ev.Error)
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintln(&b)
	}
	if len(runEvents) > 0 {
		fmt.Fprintln(&b, "## Run events")
		for _, ev := range runEvents {
			fmt.Fprintf(&b, "- `%d` `%s`", ev.Seq, ev.Type)
			if ev.Tool != "" {
				fmt.Fprintf(&b, " tool=`%s`", ev.Tool)
			}
			if ev.Message != "" {
				fmt.Fprintf(&b, " message=%s", ev.Message)
			}
			if ev.Error != "" {
				fmt.Fprintf(&b, " error=%s", ev.Error)
			}
			fmt.Fprintln(&b)
		}
	}
	return strings.TrimSpace(b.String())
}

func requestCorrelationTranscriptMarkdown(requestID string, transcripts []RunTranscriptResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# kkode request transcript\n\n")
	fmt.Fprintf(&b, "- request: `%s`\n", requestID)
	fmt.Fprintf(&b, "- runs: `%d`\n\n", len(transcripts))
	for i, transcript := range transcripts {
		fmt.Fprintf(&b, "## Run %d `%s`\n\n", i+1, transcript.Run.ID)
		if strings.TrimSpace(transcript.Markdown) != "" {
			fmt.Fprintln(&b, strings.TrimSpace(transcript.Markdown))
			fmt.Fprintln(&b)
		}
	}
	return strings.TrimSpace(b.String())
}

func sessionTranscriptMarkdown(sess *session.Session, turns []TurnDTO) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# kkode session transcript\n\n")
	fmt.Fprintf(&b, "- session: `%s`\n", sess.ID)
	fmt.Fprintf(&b, "- provider: `%s`\n", sess.ProviderName)
	fmt.Fprintf(&b, "- model: `%s`\n", sess.Model)
	if sess.Summary != "" {
		fmt.Fprintf(&b, "- summary: %s\n", strings.TrimSpace(sess.Summary))
	}
	fmt.Fprintln(&b)
	for _, turn := range turns {
		fmt.Fprintf(&b, "## Turn %d `%s`\n\n", turn.Seq, turn.ID)
		fmt.Fprintf(&b, "**User**\n\n%s\n\n", strings.TrimSpace(turn.Prompt))
		if strings.TrimSpace(turn.ResponseText) != "" {
			fmt.Fprintf(&b, "**Assistant**\n\n%s\n\n", strings.TrimSpace(turn.ResponseText))
		}
		if turn.Error != "" {
			fmt.Fprintf(&b, "**Error**\n\n%s\n\n", strings.TrimSpace(turn.Error))
		}
	}
	return strings.TrimSpace(b.String())
}
