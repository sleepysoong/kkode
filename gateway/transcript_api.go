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
	Session  SessionDTO `json:"session"`
	Turns    []TurnDTO  `json:"turns"`
	Markdown string     `json:"markdown"`
	Redacted bool       `json:"redacted"`
}

func (s *Server) getSessionTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 transcript method예요")
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	redacted := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("redact")), "false")
	resp := toTranscriptResponse(sess, redacted)
	writeJSON(w, resp)
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
