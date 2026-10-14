package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Hot roots are kept at the rate, every other root always; children follow
// their parent's decision, whether it was made here or in another process.
func TestSampler(t *testing.T) {
	tracer := func(rate Rate) trace.Tracer {
		return sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler(rate))).Tracer("test")
	}
	sampled := func(ctx context.Context, tr trace.Tracer, opts ...trace.SpanStartOption) (context.Context, bool) {
		ctx, span := tr.Start(ctx, "span", opts...)
		defer span.End()
		return ctx, span.SpanContext().IsSampled()
	}
	ctx := t.Context()
	none, all := tracer(0), tracer(1)

	if _, ok := sampled(ctx, none, trace.WithAttributes(hotAttr.Key.Bool(false))); !ok {
		t.Error("a root that isn't hot was dropped at rate 0")
	}
	dropped, ok := sampled(ctx, none, Hot)
	if ok {
		t.Error("a hot root was kept at rate 0")
	}
	kept, ok := sampled(ctx, all, Hot)
	if !ok {
		t.Error("a hot root was dropped at rate 1")
	}
	if _, ok := sampled(dropped, all); ok {
		t.Error("the child of a dropped root was kept")
	}
	if _, ok := sampled(kept, none, Hot); !ok {
		t.Error("the hot child of a kept root was dropped")
	}

	// A parent from another process (traceparent): its flag decides.
	remote := func(flags trace.TraceFlags) context.Context {
		return trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: flags, Remote: true,
		}))
	}
	if _, ok := sampled(remote(0), all); ok {
		t.Error("the child of an unsampled remote parent was kept")
	}
	if _, ok := sampled(remote(trace.FlagsSampled), none, Hot); !ok {
		t.Error("the child of a sampled remote parent was dropped")
	}
}

func TestRateParsing(t *testing.T) {
	for in, want := range map[string]Rate{"0": 0, "0.25": 0.25, "1": 1} {
		var r Rate
		if err := r.UnmarshalText([]byte(in)); err != nil || r != want {
			t.Errorf("%q = %v, %v; want %v", in, r, err, want)
		}
	}
	for _, in := range []string{"", "-0.1", "1.5", "half"} {
		var r Rate
		if err := r.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
