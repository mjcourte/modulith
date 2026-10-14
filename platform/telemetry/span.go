package telemetry

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// End ends span, marking it failed if *err is set when it runs: use it with
// a named error result, as `defer telemetry.End(span, &err)`, so it sees
// what the function finally returns. Only for work whose errors are all
// failures (loads, saves, background jobs); a request's refusals (forbidden,
// not found) are expected outcomes, recorded by its transport span instead.
func End(span trace.Span, err *error) {
	if err != nil && *err != nil {
		span.RecordError(*err)
		span.SetStatus(codes.Error, (*err).Error())
	}
	span.End()
}
