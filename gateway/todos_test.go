package gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/sleepysoong/kkode/session"
)

func TestSaveTodosUsesDedicatedStoreWithoutReload(t *testing.T) {
	store := &trackingGatewayTodoStore{sess: session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)}
	srv := &Server{cfg: Config{Store: store}}

	err := srv.saveTodos(context.Background(), store.sess.ID, []session.Todo{{ID: "todo_1", Content: "ship", Status: session.TodoPending}})
	if err != nil {
		t.Fatal(err)
	}
	if store.saveTodosCalls != 1 || store.loadSessionCalls != 0 || store.saveSessionCalls != 0 {
		t.Fatalf("dedicated SaveTodos should avoid full session load/save: load=%d saveTodos=%d saveSession=%d", store.loadSessionCalls, store.saveTodosCalls, store.saveSessionCalls)
	}
	if len(store.sess.Todos) != 1 || store.sess.Todos[0].ID != "todo_1" {
		t.Fatalf("todos not saved: %+v", store.sess.Todos)
	}
}

type trackingGatewayTodoStore struct {
	sess             *session.Session
	loadSessionCalls int
	saveTodosCalls   int
	saveSessionCalls int
}

func (s *trackingGatewayTodoStore) CreateSession(ctx context.Context, sess *session.Session) error {
	s.sess = sess
	return nil
}

func (s *trackingGatewayTodoStore) LoadSession(ctx context.Context, id string) (*session.Session, error) {
	s.loadSessionCalls++
	if s.sess == nil || s.sess.ID != id {
		return nil, fmt.Errorf("session not found")
	}
	clone := *s.sess
	clone.Todos = append([]session.Todo(nil), s.sess.Todos...)
	return &clone, nil
}

func (s *trackingGatewayTodoStore) SaveSession(ctx context.Context, sess *session.Session) error {
	s.saveSessionCalls++
	s.sess = sess
	return nil
}

func (s *trackingGatewayTodoStore) ListSessions(ctx context.Context, q session.SessionQuery) ([]session.SessionSummary, error) {
	return nil, nil
}

func (s *trackingGatewayTodoStore) AppendEvent(ctx context.Context, ev session.Event) error {
	return nil
}

func (s *trackingGatewayTodoStore) SaveCheckpoint(ctx context.Context, cp session.Checkpoint) error {
	return nil
}

func (s *trackingGatewayTodoStore) Close() error { return nil }

func (s *trackingGatewayTodoStore) SaveTodos(ctx context.Context, sessionID string, todos []session.Todo) error {
	s.saveTodosCalls++
	if s.sess == nil || s.sess.ID != sessionID {
		return fmt.Errorf("session not found")
	}
	s.sess.Todos = append([]session.Todo(nil), todos...)
	return nil
}
