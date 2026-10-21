package postgres_test

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mjcourte/modulith/platform/postgres"
)

func TestConnectAppliesSessionSettings(t *testing.T) {
	url := os.Getenv("MODULITH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_DATABASE_URL to run against Postgres")
	}
	pool, err := postgres.Connect(t.Context(), url, "platform-test")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	for setting, want := range map[string]string{
		"plan_cache_mode":  "force_custom_plan",
		"application_name": "platform-test",
	} {
		var got string
		if err := pool.QueryRow(t.Context(), "SELECT current_setting($1)", setting).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", setting, got, want)
		}
	}
}

func TestConnectFailsFastOnBadURL(t *testing.T) {
	// Nothing listens on port 1, so the startup ping must fail.
	_, err := postgres.Connect(t.Context(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1", "platform-test")
	if err == nil {
		t.Fatal("Connect succeeded against a closed port")
	}
}

// Every query inside a trace gets a span named after its sqlc query ("--
// name: X"), or its first word, never its SQL (too long and varied to name
// spans by). Queries outside a trace (migrations, untraced jobs) get none.
func TestQueriesAreTraced(t *testing.T) {
	url := os.Getenv("MODULITH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_DATABASE_URL to run against Postgres")
	}
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	pool, err := postgres.Connect(t.Context(), url, "platform-test")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	var n int
	if err := pool.QueryRow(t.Context(), "-- name: Untraced :one\nSELECT 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	ctx, parent := otel.Tracer("test").Start(t.Context(), "request")
	for _, sql := range []string{"-- name: CountOne :one\nSELECT 1", "\n  select 1"} {
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	parent.End()
	names := spanNames(spans)
	if names["Untraced"] {
		t.Error("a query outside any trace made a span")
	}
	for _, want := range []string{"CountOne", "SELECT"} {
		if !names[want] {
			t.Errorf("no span named %q among %v", want, names)
		}
	}
}

func spanNames(r *tracetest.SpanRecorder) map[string]bool {
	names := map[string]bool{}
	for _, s := range r.Ended() {
		names[s.Name()] = true
	}
	return names
}
