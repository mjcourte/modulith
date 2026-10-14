package ws_test

import (
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"gyoza/services/iam"
)

// spans is the process's span recorder. OTel's global tracer provider binds
// package tracers to the first provider set, so it's set once.
var spans = sync.OnceValue(func() *tracetest.SpanRecorder {
	r := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r)))
	return r
})

// The volume and error rules for message spans: edits are a hot path
// (sampled at the hot rate), presence makes no span, and a refusal is
// recorded as refused, not as a failure.
func TestMessageSpans(t *testing.T) {
	rec := spans()
	before := len(rec.Ended())
	w := newWorld(t)
	alice, bob := w.signIn("alice"), w.signIn("bob")
	p := w.project(alice, map[string]iam.Access{bob: iam.AccessViewer})
	stranger := w.signIn("eve")
	a, e := w.connect(alice, nil), w.connect(stranger, nil)

	a.join(p)
	a.send(map[string]any{"type": "userstate", "projectId": p, "kind": "viewing"})
	a.expect("userstate") // presence comes back to its sender too
	a.send(edit(t, p, "e1", a.replica, uint64(time.Now().UnixMilli())<<16))
	a.expect("delta") // the fan-out is queued before the ack
	a.expect("ack")
	e.send(map[string]any{"type": "join", "projectId": p})
	e.expectError("FORBIDDEN")
	a.sync() // every message above is handled, its span ended
	e.sync()

	got := map[string][]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended()[before:] {
		got[s.Name()] = append(got[s.Name()], s)
	}
	hot := attribute.Bool("gyoza.hot", true)
	has := func(s sdktrace.ReadOnlySpan, kv attribute.KeyValue) bool {
		for _, a := range s.Attributes() {
			if a == kv {
				return true
			}
		}
		return false
	}
	if len(got["ws.userstate"]) != 0 {
		t.Error("presence made a span")
	}
	if edits := got["ws.edit"]; len(edits) != 1 || !has(edits[0], hot) {
		t.Errorf("edit spans %v, want one marked hot", edits)
	}
	joins := got["ws.join"]
	if len(joins) != 2 {
		t.Fatalf("got %d join spans, want 2", len(joins))
	}
	for _, j := range joins {
		if has(j, hot) {
			t.Error("a join is marked hot: joins are always kept")
		}
		if j.Status().Code == codes.Error {
			t.Errorf("join span marked failed: %v (a refusal isn't a failure)", j.Status())
		}
	}
	refused := 0
	for _, j := range joins {
		if has(j, attribute.String("gyoza.refused", "FORBIDDEN")) {
			refused++
		}
	}
	if refused != 1 {
		t.Errorf("%d join spans record the refusal, want the stranger's", refused)
	}
}
