package ws_test

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// reader is the process's metric reader. Like the tracer provider, OTel's
// global meter provider binds package instruments to the first one set.
var reader = sync.OnceValue(func() *sdkmetric.ManualReader {
	r := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(r)))
	return r
})

// Open sockets are counted, and messages are counted by type, with any type
// a browser invents counted as "unknown": a client mustn't create metric
// series at will.
func TestMetrics(t *testing.T) {
	r := reader()
	w := newWorld(t)
	alice := w.signIn("alice")
	a, b := w.connect(alice, nil), w.connect(alice, nil)
	a.send(map[string]any{"type": "made-up-1"})
	a.expectError("BAD_REQUEST")
	b.send(map[string]any{"type": "made-up-2"})
	b.expectError("BAD_REQUEST")

	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var sockets int64 = -1
	types := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				if m.Name == "gyoza.ws.sockets" && len(data.DataPoints) == 1 {
					sockets = data.DataPoints[0].Value
				}
			case metricdata.Sum[int64]:
				if m.Name == "gyoza.ws.messages" {
					for _, p := range data.DataPoints {
						typ, _ := p.Attributes.Value(attribute.Key("type"))
						types[typ.AsString()] += p.Value
					}
				}
			}
		}
	}
	if sockets != 2 {
		t.Errorf("gyoza.ws.sockets = %d, want 2", sockets)
	}
	if types["unknown"] < 2 || types["made-up-1"] != 0 || types["made-up-2"] != 0 {
		t.Errorf("messages by type %v: want the made-up types counted as unknown", types)
	}
}
