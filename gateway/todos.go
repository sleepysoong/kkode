package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

type todoPersistStore interface {
	LoadSession(ctx context.Context, id string) (*session.Session, error)
	SaveTodos(ctx context.Context, sessionID string, todos []session.Todo) error
}

func (s *Server) handleSessionTodos(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.getSessionTodos(w, r, sessionID)
		case http.MethodPut:
			s.replaceSessionTodos(w, r, sessionID)
		case http.MethodPost:
			s.upsertSessionTodo(w, r, sessionID)
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 todo method예요")
		}
		return
	}
	if len(rest) == 1 && r.Method == http.MethodDelete {
		s.deleteSessionTodo(w, r, sessionID, rest[0])
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "todo endpoint를 찾을 수 없어요")
}

func (s *Server) getSessionTodos(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	writeJSON(w, TodoListResponse{Todos: todoDTOs(sess.Todos)})
}

func (s *Server) replaceSessionTodos(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req TodoListResponse
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	todos, err := todosFromDTOs(req.Todos, s.cfg.Now())
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_todo", err.Error())
		return
	}
	if err := s.saveTodos(r.Context(), sessionID, todos); err != nil {
		writeError(w, r, http.StatusNotFound, "save_todos_failed", err.Error())
		return
	}
	writeJSON(w, TodoListResponse{Todos: todoDTOs(todos)})
}

func (s *Server) upsertSessionTodo(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req TodoDTO
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	todo, err := todoFromDTO(req, s.cfg.Now())
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_todo", err.Error())
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	upsertTodo(&sess.Todos, todo)
	if err := s.saveTodos(r.Context(), sessionID, sess.Todos); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_todos_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, TodoListResponse{Todos: todoDTOs(sess.Todos)})
}

func (s *Server) deleteSessionTodo(w http.ResponseWriter, r *http.Request, sessionID string, todoID string) {
	todoID = strings.TrimSpace(todoID)
	if todoID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_todo", "todo id가 필요해요")
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	filtered := sess.Todos[:0]
	removed := false
	for _, todo := range sess.Todos {
		if todo.ID == todoID {
			removed = true
			continue
		}
		filtered = append(filtered, todo)
	}
	if !removed {
		writeError(w, r, http.StatusNotFound, "todo_not_found", "todo를 찾을 수 없어요")
		return
	}
	if err := s.saveTodos(r.Context(), sessionID, filtered); err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_todos_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveTodos(ctx context.Context, sessionID string, todos []session.Todo) error {
	sess, err := s.cfg.Store.LoadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if store, ok := s.cfg.Store.(todoPersistStore); ok {
		return store.SaveTodos(ctx, sessionID, todos)
	}
	sess.Todos = todos
	sess.Touch()
	return s.cfg.Store.SaveSession(ctx, sess)
}

func todosFromDTOs(dtos []TodoDTO, now time.Time) ([]session.Todo, error) {
	out := make([]session.Todo, 0, len(dtos))
	for _, dto := range dtos {
		todo, err := todoFromDTO(dto, now)
		if err != nil {
			return nil, err
		}
		out = append(out, todo)
	}
	return out, nil
}

func todoFromDTO(dto TodoDTO, now time.Time) (session.Todo, error) {
	content := strings.TrimSpace(dto.Content)
	if content == "" {
		return session.Todo{}, fmt.Errorf("todo content가 필요해요")
	}
	status := session.TodoStatus(strings.TrimSpace(dto.Status))
	if status == "" {
		status = session.TodoPending
	}
	if !validTodoStatus(status) {
		return session.Todo{}, fmt.Errorf("지원하지 않는 todo status예요: %s", status)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updatedAt := dto.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now.UTC()
	}
	id := strings.TrimSpace(dto.ID)
	if id == "" {
		id = session.NewID("todo")
	}
	return session.Todo{ID: id, Content: content, Status: status, Priority: strings.TrimSpace(dto.Priority), UpdatedAt: updatedAt.UTC()}, nil
}

func validTodoStatus(status session.TodoStatus) bool {
	switch status {
	case session.TodoPending, session.TodoInProgress, session.TodoCompleted, session.TodoCancelled:
		return true
	default:
		return false
	}
}

func upsertTodo(todos *[]session.Todo, item session.Todo) {
	for i := range *todos {
		if (*todos)[i].ID == item.ID {
			(*todos)[i] = item
			return
		}
	}
	*todos = append(*todos, item)
}

func todoDTOs(todos []session.Todo) []TodoDTO {
	out := make([]TodoDTO, 0, len(todos))
	for _, todo := range todos {
		out = append(out, toTodoDTO(todo))
	}
	return out
}
