package nats_test

import (
	"reflect"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/mjcourte/modulith/platform/nats"
)

func TestDeltaRoundTrip(t *testing.T) {
	for _, in := range []nats.Delta{
		{ProjectID: "p1", ID: "m1", ActorID: "ann", Origin: "docs-1", Format: "native", Version: 2, Payload: []byte{1, 2}},
		// A browser's delta, published by the gateway: no origin.
		{ProjectID: "p1", ID: "m2", ActorID: "ann", Format: "native", Version: 1, Payload: []byte{3}},
	} {
		msg, err := in.Msg(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if in.Origin == "" && msg.Header.Get(nats.HeaderOrigin) != "" {
			t.Errorf("empty origin was set: %q", msg.Header.Get(nats.HeaderOrigin))
		}
		got, err := nats.DecodeDelta(msg.Subject, 9, msg.Header, msg.Data)
		if err != nil {
			t.Fatal(err)
		}
		in.Seq = 9
		if !reflect.DeepEqual(got, in) {
			t.Errorf("got %+v, want %+v", got, in)
		}
	}
}

func TestDecodeDeltaRejects(t *testing.T) {
	good, _ := nats.Delta{ProjectID: "p1", Format: "native", Version: 1}.Msg(t.Context())
	if _, err := nats.DecodeDelta("document.p1.snapshot", 1, good.Header, nil); err == nil {
		t.Error("snapshot subject accepted as a delta")
	}
	noFormat, _ := nats.Delta{ProjectID: "p1", Version: 1}.Msg(t.Context())
	if _, err := nats.DecodeDelta(noFormat.Subject, 1, noFormat.Header, nil); err == nil {
		t.Error("delta without a format accepted")
	}
}

func TestParseUserState(t *testing.T) {
	if p, u, k, ok := nats.ParseUserState("userstate.p1.ann.cursorpos"); !ok || p != "p1" || u != "ann" || k != "cursorpos" {
		t.Errorf("got %q %q %q %v", p, u, k, ok)
	}
	for _, s := range []string{"userstate.p1.ann.other", "userstate.p1.ann", "document.p1.delta", "userstate..ann.viewing"} {
		if _, _, _, ok := nats.ParseUserState(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}

// A delta carries its publisher's trace: a reader gets the same span context
// back from the headers. Without a trace, nothing is added.
func TestDeltaCarriesTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	_, span := sdktrace.NewTracerProvider().Tracer("test").Start(t.Context(), "edit")
	defer span.End()
	ctx := trace.ContextWithSpan(t.Context(), span)

	msg, err := nats.Delta{ProjectID: "p1", Format: "native", Version: 1}.Msg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := trace.SpanContextFromContext(nats.TraceContext(t.Context(), msg.Header))
	if !got.IsRemote() || got.TraceID() != span.SpanContext().TraceID() || got.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("read back %v, want the publisher's %v", got, span.SpanContext())
	}

	plain, _ := nats.Delta{ProjectID: "p1", Format: "native", Version: 1}.Msg(t.Context())
	if sc := trace.SpanContextFromContext(nats.TraceContext(t.Context(), plain.Header)); sc.IsValid() {
		t.Errorf("a delta published outside a trace carries one: %v", sc)
	}
}
