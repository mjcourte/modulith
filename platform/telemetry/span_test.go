package telemetry

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// End marks a span failed only when the function returned an error, as seen
// through a named result when the deferred call runs.
func TestEnd(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	run := func(fail bool) (err error) {
		_, span := tracer.Start(t.Context(), "work")
		defer End(span, &err)
		err = errors.New("first attempt") // overwritten before returning: not a failure
		if fail {
			return errors.New("broken")
		}
		return nil
	}
	run(false)
	run(true)

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("%d spans ended, want 2", len(ended))
	}
	if ok := ended[0].Status(); ok.Code == codes.Error {
		t.Errorf("a successful run is marked failed: %v", ok)
	}
	if failed := ended[1].Status(); failed.Code != codes.Error || failed.Description != "broken" {
		t.Errorf("a failed run's status = %v, want Error \"broken\"", failed)
	}
}
