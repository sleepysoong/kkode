package session

import (
	"context"
	"fmt"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

func Fork(ctx context.Context, store Store, sourceID string, atTurnID string) (*Session, error) {
	source, err := store.LoadSession(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	forked, err := ForkSession(source, atTurnID)
	if err != nil {
		return nil, err
	}
	if err := store.CreateSession(ctx, forked); err != nil {
		return nil, err
	}
	return forked, nil
}

func ForkSession(source *Session, atTurnID string) (*Session, error) {
	if source == nil {
		return nil, fmt.Errorf("source session is nil")
	}
	now := time.Now().UTC()
	forked := *source
	forked.ID = NewID("sess")
	forked.CreatedAt = now
	forked.UpdatedAt = now
	forked.Metadata = map[string]string{}
	for k, v := range source.Metadata {
		forked.Metadata[k] = v
	}
	forked.Metadata["forked_from"] = source.ID
	if atTurnID != "" {
		forked.Metadata["forked_at_turn"] = atTurnID
	}
	forked.Turns = retainedTurns(source.Turns, atTurnID)
	forked.Events = retainedEvents(source.Events, forked.ID, retainedTurnIDs(forked.Turns))
	forked.Todos = cloneTodos(source.Todos)
	forked.LastResponseID = ""
	forked.LastInputItems = nil
	if len(forked.Turns) > 0 {
		last := forked.Turns[len(forked.Turns)-1]
		if last.Response != nil {
			forked.LastResponseID = last.Response.ID
			forked.LastInputItems = append([]llm.Item{}, last.Response.Output...)
		}
	}
	return &forked, nil
}

func retainedTurns(turns []Turn, atTurnID string) []Turn {
	if atTurnID == "" {
		out := append([]Turn{}, turns...)
		return out
	}
	out := []Turn{}
	for _, turn := range turns {
		out = append(out, turn)
		if turn.ID == atTurnID {
			break
		}
	}
	return out
}

func retainedEvents(events []Event, newSessionID string, retained map[string]bool) []Event {
	out := []Event{}
	if len(retained) == 0 {
		return out
	}
	for _, ev := range events {
		if ev.TurnID == "" || retained[ev.TurnID] {
			ev.ID = NewID("ev")
			ev.SessionID = newSessionID
			out = append(out, ev)
		}
	}
	return out
}

func retainedTurnIDs(turns []Turn) map[string]bool {
	out := map[string]bool{}
	for _, turn := range turns {
		if turn.ID != "" {
			out[turn.ID] = true
		}
	}
	return out
}

func cloneTodos(todos []Todo) []Todo {
	out := make([]Todo, len(todos))
	copy(out, todos)
	return out
}
