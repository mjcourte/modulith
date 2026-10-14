package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// StartDebug serves Go's profiles (net/http/pprof) on addr, for
// `go tool pprof http://<addr>/debug/pprof/profile` (CPU) and .../heap. Empty
// addr: nothing. Profiles expose the process's internals, so keep addr on
// localhost (or reachable only through a port-forward). stop shuts the
// listener down within ctx.
func StartDebug(addr string) (stop func(context.Context) error, err error) {
	if addr == "" {
		return func(context.Context) error { return nil }, nil
	}
	// Its own mux: importing net/http/pprof also registers these on
	// http.DefaultServeMux, which no server here uses.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// No write timeout: a CPU profile streams for as long as it's asked (30s
	// by default).
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(lis); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("telemetry: debug server stopped", "err", err)
		}
	}()
	slog.Info("telemetry: serving pprof", "addr", lis.Addr().String())
	return srv.Shutdown, nil
}
