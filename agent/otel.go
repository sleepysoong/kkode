package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const defaultTraceInstrumentationName = "github.com/sleepysoong/kkode/agent"

func OTelObserver(tracer trace.Tracer) Observer {
	if tracer == nil {
		return ObserverFunc(func(context.Context, TraceEvent) {})
	}
	return ObserverFunc(func(ctx context.Context, event TraceEvent) {
		_, span := tracer.Start(ctx, traceEventSpanName(event), trace.WithAttributes(traceEventAttributes(event)...))
		defer span.End()
		if event.Error != "" {
			errText := llm.RedactSecrets(event.Error)
			span.RecordError(errors.New(errText))
			span.SetStatus(codes.Error, errText)
		}
	})
}

func GlobalOTelObserver(instrumentationName string) Observer {
	if strings.TrimSpace(instrumentationName) == "" {
		instrumentationName = defaultTraceInstrumentationName
	}
	return OTelObserver(otel.Tracer(instrumentationName))
}

func traceEventSpanName(event TraceEvent) string {
	eventType := strings.TrimSpace(event.Type)
	if eventType == "" {
		eventType = "agent.event"
	}
	if tool := strings.TrimSpace(event.Tool); tool != "" {
		return eventType + " " + tool
	}
	return eventType
}

func traceEventAttributes(event TraceEvent) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("kkode.trace.type", strings.TrimSpace(event.Type)),
	}
	if !event.At.IsZero() {
		attrs = append(attrs, attribute.String("kkode.trace.at", event.At.Format("2006-01-02T15:04:05.000000000Z07:00")))
	}
	if tool := strings.TrimSpace(event.Tool); tool != "" {
		attrs = append(attrs, attribute.String("kkode.trace.tool", tool))
	}
	if message := strings.TrimSpace(event.Message); message != "" {
		attrs = append(attrs, attribute.String("kkode.trace.message", llm.RedactSecrets(message)))
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		attrs = append(attrs, attribute.String("kkode.trace.error", llm.RedactSecrets(errText)))
	}
	return attrs
}
