package agent

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"
)

func TestOTelObserverExportsRedactedTraceEvent(t *testing.T) {
	event := TraceEvent{Type: "tool.completed", Tool: "shell_run", Message: "token=abc1234567890secretvalue", Error: "error token=abc1234567890secretvalue"}
	if got := traceEventSpanName(event); got != "tool.completed shell_run" {
		t.Fatalf("span name = %q", got)
	}
	attrs := traceEventAttributes(event)
	var sawRedacted bool
	for _, attr := range attrs {
		if attr.Value.AsString() == "[REDACTED]" || attr.Value.AsString() == "error [REDACTED]" {
			sawRedacted = true
		}
		if attr.Value.AsString() == "token=abc1234567890secretvalue" {
			t.Fatalf("trace attributes must redact secrets: %+v", attrs)
		}
	}
	if !sawRedacted {
		t.Fatalf("trace attributes should include redacted values: %+v", attrs)
	}

	observer := OTelObserver(noop.NewTracerProvider().Tracer("kkode-test"))
	observer.OnEvent(context.Background(), event)
	GlobalOTelObserver("").OnEvent(context.Background(), TraceEvent{Type: "agent.completed"})
}
