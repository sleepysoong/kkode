package gateway

import "context"

type runEventReporterKey struct{}

// RunEventReporter는 실행 중 agent/tool trace를 background run event로 흘려보내는 내부 callback이에요.
type RunEventReporter func(ctx context.Context, event RunEventDTO)

// WithRunEventReporter는 RunStarter context에 durable run event reporter를 심어요.
func WithRunEventReporter(ctx context.Context, reporter RunEventReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, runEventReporterKey{}, reporter)
}

// ReportRunEvent는 RunStarter 안에서 agent/tool trace를 run event로 기록해요.
// reporter가 없는 context면 false를 반환하고 아무 일도 하지 않아요.
func ReportRunEvent(ctx context.Context, event RunEventDTO) bool {
	reporter, _ := ctx.Value(runEventReporterKey{}).(RunEventReporter)
	if reporter == nil {
		return false
	}
	reporter(ctx, event)
	return true
}
