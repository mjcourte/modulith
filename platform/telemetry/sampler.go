package telemetry

import (
	"slices"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Hot marks a root span on a high-volume path (a WebSocket edit, a delta
// applied): it's sampled at Settings.HotRate, every other root is kept.
// Pass it when starting the span (tracer.Start(ctx, name, telemetry.Hot)):
// the sampler only sees attributes given at start. Children follow their
// root's decision, in this process and every other the trace reaches.
var Hot = trace.WithAttributes(hotAttr)

var hotAttr = attribute.Bool("modulith.hot", true)

// sampler keeps every root except Hot ones, which it keeps at rate; a child
// span follows its parent, so a trace is kept or dropped whole.
func sampler(rate Rate) sdktrace.Sampler {
	return sdktrace.ParentBased(hotPath{rate: sdktrace.TraceIDRatioBased(float64(rate))})
}

type hotPath struct{ rate sdktrace.Sampler }

func (s hotPath) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if slices.Contains(p.Attributes, hotAttr) {
		return s.rate.ShouldSample(p)
	}
	return sdktrace.AlwaysSample().ShouldSample(p)
}

func (s hotPath) Description() string { return "HotPath{" + s.rate.Description() + "}" }
