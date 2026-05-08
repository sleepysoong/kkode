package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

func TestGatewayCompactionUsesIncrementalSessionStateSave(t *testing.T) {
	store := &trackingCompactStore{sess: session.NewSession("/repo", "openai", "gpt-5-mini", "web", session.AgentModeBuild)}
	for _, prompt := range []string{"첫 요청", "둘째 요청", "셋째 요청"} {
		turn := session.NewTurn(prompt, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText(prompt)}})
		turn.Response = llm.TextResponse("openai", "gpt-5-mini", prompt+" 응답")
		turn.EndedAt = turn.StartedAt.Add(time.Second)
		store.sess.AppendTurn(turn)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+store.sess.ID+"/compact", bytes.NewBufferString(`{"preserve_first_n_turns":1,"preserve_last_n_turns":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got SessionCompactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary == "" || !strings.Contains(got.Summary, "둘째 요청") {
		t.Fatalf("compact summary가 이상해요: %+v", got)
	}
	if store.saveSessionStateCalls != 1 || store.saveSessionCalls != 0 {
		t.Fatalf("compaction should save only session state on incremental stores: state=%d full=%d", store.saveSessionStateCalls, store.saveSessionCalls)
	}
}

type trackingCompactStore struct {
	sess                  *session.Session
	saveSessionCalls      int
	saveSessionStateCalls int
}

func (s *trackingCompactStore) CreateSession(ctx context.Context, sess *session.Session) error {
	s.sess = sess
	return nil
}

func (s *trackingCompactStore) LoadSession(ctx context.Context, id string) (*session.Session, error) {
	if s.sess == nil || s.sess.ID != id {
		return nil, fmt.Errorf("session not found")
	}
	clone := *s.sess
	clone.Turns = append([]session.Turn(nil), s.sess.Turns...)
	clone.Events = append([]session.Event(nil), s.sess.Events...)
	clone.Todos = append([]session.Todo(nil), s.sess.Todos...)
	return &clone, nil
}

func (s *trackingCompactStore) SaveSession(ctx context.Context, sess *session.Session) error {
	s.saveSessionCalls++
	s.sess = sess
	return nil
}

func (s *trackingCompactStore) ListSessions(ctx context.Context, q session.SessionQuery) ([]session.SessionSummary, error) {
	return nil, nil
}

func (s *trackingCompactStore) AppendEvent(ctx context.Context, ev session.Event) error {
	return nil
}

func (s *trackingCompactStore) SaveCheckpoint(ctx context.Context, cp session.Checkpoint) error {
	return nil
}

func (s *trackingCompactStore) Close() error { return nil }

func (s *trackingCompactStore) AppendTurn(ctx context.Context, sessionID string, turn session.Turn) error {
	if s.sess == nil || s.sess.ID != sessionID {
		return fmt.Errorf("session not found")
	}
	s.sess.Turns = append(s.sess.Turns, turn)
	return nil
}

func (s *trackingCompactStore) SaveSessionState(ctx context.Context, sess *session.Session) error {
	s.saveSessionStateCalls++
	if s.sess == nil || s.sess.ID != sess.ID {
		return fmt.Errorf("session not found")
	}
	s.sess.Summary = sess.Summary
	s.sess.LastResponseID = sess.LastResponseID
	s.sess.LastInputItems = append([]llm.Item(nil), sess.LastInputItems...)
	s.sess.Metadata = llm.CloneMetadata(sess.Metadata)
	s.sess.UpdatedAt = sess.UpdatedAt
	return nil
}
