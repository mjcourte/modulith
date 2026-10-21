// Package telemetry sends traces, logs and metrics to an OpenTelemetry
// collector (eg. SigNoz) over OTLP/gRPC. Plan: PLAN.md.
package telemetry

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"google.golang.org/grpc"
)

// Telemetry is one binary's exporters. The zero value (no endpoint) exports
// nothing: OTel's global providers stay no-ops, so instrumented code costs
// ~nothing.
type Telemetry struct {
	logs     *sdklog.LoggerProvider // nil when off
	shutdown []func(context.Context) error
}

// Setup installs global tracer, logger and meter providers exporting to
// s.Endpoint (eg. "http://localhost:4317"; http:// means no TLS), sampling hot
// paths at s.HotRate (Hot), and starts the Go runtime metrics. With no endpoint
// it does nothing. Call Shutdown last: it flushes what's buffered.
func Setup(ctx context.Context, service string, s Settings) (*Telemetry, error) {
	if s.Endpoint == "" {
		return &Telemetry{}, nil
	}
	res, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName(service)),
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}
	// Exporters connect lazily, so these don't fail on a collector that's
	// down: exports then fail (and are reported) instead.
	spanExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(s.Endpoint))
	if err != nil {
		return nil, err
	}
	logExp, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpointURL(s.Endpoint))
	if err != nil {
		return nil, errors.Join(err, spanExp.Shutdown(ctx))
	}
	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(s.Endpoint))
	if err != nil {
		return nil, errors.Join(err, spanExp.Shutdown(ctx), logExp.Shutdown(ctx))
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spanExp), sdktrace.WithResource(res), sdktrace.WithSampler(sampler(s.HotRate)))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)), sdklog.WithResource(res))
	// Metrics are aggregated in the process and sent once a minute, so their
	// cost doesn't grow with traffic.
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)), sdkmetric.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// How a trace crosses process boundaries: the W3C traceparent header.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	// Go runtime metrics: memory, GC, goroutines.
	if err := runtime.Start(); err != nil {
		return nil, errors.Join(err, tp.Shutdown(ctx), lp.Shutdown(ctx), mp.Shutdown(ctx))
	}
	return &Telemetry{logs: lp, shutdown: []func(context.Context) error{tp.Shutdown, lp.Shutdown, mp.Shutdown}}, nil
}

// LogHandler returns h, plus (when exporting) the same records at level and
// above sent to the collector, attached to the span in the record's context
// (slog.InfoContext and the like; calls without a context attach to none).
func (t *Telemetry) LogHandler(h slog.Handler, level slog.Leveler) slog.Handler {
	if t.logs == nil {
		return h
	}
	otlp := otelslog.NewHandler("modulith", otelslog.WithLoggerProvider(t.logs))
	return slog.NewMultiHandler(h, minLevel{otlp, level})
}

// Shutdown flushes and stops the exporters, within ctx's deadline.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}

// ServerOption gives a gRPC server one span per call, continuing the
// caller's trace when the call carries one.
func ServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler())
}

// DialOption gives a gRPC client one span per call and sends the trace on,
// so the server's span joins it.
func DialOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler())
}

// minLevel drops records below level: the OTel bridge would export every
// level, debug included, whatever the text handler shows.
type minLevel struct {
	slog.Handler
	level slog.Leveler
}

func (h minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.Handler.Enabled(ctx, l)
}

// WithAttrs and WithGroup keep the filter on derived handlers (slog.With).
func (h minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevel{h.Handler.WithAttrs(attrs), h.level}
}

func (h minLevel) WithGroup(name string) slog.Handler {
	return minLevel{h.Handler.WithGroup(name), h.level}
}
