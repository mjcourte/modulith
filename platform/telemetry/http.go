package telemetry

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HTTPHandler gives h one span per request, continuing the caller's trace,
// except for requests skip matches (a WebSocket upgrade: a span lasting the
// socket's lifetime says nothing). Spans are named "METHOD" until SetRoute
// names the route: raw paths would make every project ID a span name.
func HTTPHandler(h http.Handler, skip func(*http.Request) bool) http.Handler {
	return otelhttp.NewHandler(h, "http",
		otelhttp.WithFilter(func(r *http.Request) bool { return !skip(r) }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
}

// SetRoute names the request's span after the route that served it: call it
// after routing, with an http.ServeMux's r.Pattern ("GET /projects/{id}").
// prefix is what a StripPrefix in front removed ("/api").
func SetRoute(r *http.Request, prefix string) {
	method, path, ok := strings.Cut(r.Pattern, " ")
	if !ok {
		method, path = r.Method, r.Pattern
	}
	if path == "" {
		return // nothing matched: a 404 keeps the method alone
	}
	span := trace.SpanFromContext(r.Context())
	span.SetName(method + " " + prefix + path)
	span.SetAttributes(attribute.String("http.route", prefix+path))
}

// HTTPTransport gives an HTTP client one span per request and sends the
// trace on, so the server's span joins it. base nil means http.DefaultTransport.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}
