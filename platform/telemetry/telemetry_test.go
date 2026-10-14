package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

// Logs reach the collector at the configured level and above, as well as the
// text output; with telemetry off, the text handler is used as it is.
func TestLogHandler(t *testing.T) {
	text := slog.NewTextHandler(&bytes.Buffer{}, nil)
	if got := (&Telemetry{}).LogHandler(text, slog.LevelInfo); got != text {
		t.Errorf("off: LogHandler = %T, want the text handler itself", got)
	}

	var out bytes.Buffer
	exp := &memLogs{}
	tel := &Telemetry{logs: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))}
	log := slog.New(tel.LogHandler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}), slog.LevelInfo))
	log.Debug("too detailed")
	log.With("project", "p1").Info("kept")

	if got := exp.bodies(); len(got) != 1 || got[0] != "kept" {
		t.Errorf("exported %q, want only the info record", got)
	}
	if !strings.Contains(out.String(), "too detailed") || !strings.Contains(out.String(), "kept") {
		t.Errorf("text output %q lost a record: its own level applies", out.String())
	}
}

// A call through DialOption and ServerOption is one trace: the server's span
// is the client's child, across the connection.
func TestGRPCCallIsOneTrace(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(ServerOption())
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), DialOption())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	srv.GracefulStop() // the server span ends once the call is done

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("got %d spans, want the client's and the server's", len(ended))
	}
	client, server := ended[0], ended[1]
	if client.SpanKind().String() != "client" {
		client, server = server, client
	}
	if server.SpanContext().TraceID() != client.SpanContext().TraceID() || server.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Errorf("server span %v isn't the client span %v's child", server.SpanContext(), client.SpanContext())
	}
}

// memLogs is an exporter keeping log records in memory.
type memLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *memLogs) Export(_ context.Context, records []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		m.records = append(m.records, r.Clone())
	}
	return nil
}

func (m *memLogs) Shutdown(context.Context) error   { return nil }
func (m *memLogs) ForceFlush(context.Context) error { return nil }

func (m *memLogs) bodies() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.records {
		out = append(out, r.Body().AsString())
	}
	return out
}

// Behind a StripPrefix and a mux, the span is named after the route, never
// the raw path (every project ID would be a new name); skipped requests (a
// WebSocket upgrade) get none.
func TestHTTPSpanNamedAfterRoute(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(tp)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects/{id}", func(http.ResponseWriter, *http.Request) {})
	named := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		SetRoute(r, "/api")
	})
	h := HTTPHandler(http.StripPrefix("/api", named), func(r *http.Request) bool { return r.URL.Path == "/api/ws" })

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/projects/0190-abc", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/ws", nil))

	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Name() != "GET /api/projects/{id}" {
		var names []string
		for _, s := range ended {
			names = append(names, s.Name())
		}
		t.Errorf("spans %q, want one named GET /api/projects/{id} (none for the WebSocket)", names)
	}
}
