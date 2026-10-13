package grpcapi_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "gyoza/contracts/gen/go/gyoza/mediaprocessor/v1"
	"gyoza/services/mediaprocessor/internal/grpcapi"
	"gyoza/services/mediaprocessor/internal/probe"
)

// newClient serves p over an in-memory connection and returns a client for it.
func newClient(t *testing.T, p probe.Prober) *grpcapi.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterMediaProcessorServiceServer(srv, grpcapi.NewServer(p))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return grpcapi.NewClient(conn)
}

// TestProbeOverGRPC: the client returns exactly what ffprobe finds in-process.
func TestProbeOverGRPC(t *testing.T) {
	ffprobe, err := probe.NewFFprobe()
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	files := httptest.NewServer(http.FileServer(http.Dir("../probe/testdata")))
	t.Cleanup(files.Close)
	url := files.URL + "/tiny_rotated.mp4"

	want, err := ffprobe.Probe(t.Context(), url)
	if err != nil {
		t.Fatalf("Probe in-process: %v", err)
	}
	got, err := newClient(t, ffprobe).Probe(t.Context(), url)
	if err != nil {
		t.Fatalf("Probe over gRPC: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("over gRPC = %+v\nin-process = %+v", got, want)
	}
}

// proberFunc adapts a function to probe.Prober, to return chosen errors.
type proberFunc func(ctx context.Context, url string) (probe.Info, error)

func (f proberFunc) Probe(ctx context.Context, url string) (probe.Info, error) { return f(ctx, url) }

func TestProbeErrorsOverGRPC(t *testing.T) {
	t.Run("NoMedia", func(t *testing.T) {
		c := newClient(t, proberFunc(func(context.Context, string) (probe.Info, error) {
			return probe.Info{}, probe.ErrNoMedia
		}))
		if _, err := c.Probe(t.Context(), "http://x"); !errors.Is(err, probe.ErrNoMedia) {
			t.Errorf("err = %v, want ErrNoMedia", err)
		}
	})
	t.Run("FailureKeepsMessage", func(t *testing.T) {
		// Storage records the message on the file, so it must survive the trip.
		const msg = "ffprobe failed: moov atom not found"
		c := newClient(t, proberFunc(func(context.Context, string) (probe.Info, error) {
			return probe.Info{}, errors.New(msg)
		}))
		if _, err := c.Probe(t.Context(), "http://x"); err == nil || err.Error() != msg {
			t.Errorf("err = %v, want %q", err, msg)
		}
	})
	t.Run("Unreachable", func(t *testing.T) {
		// Storage keeps files queued, without using an attempt, on this error.
		conn, err := grpc.NewClient("passthrough:///unreachable",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return nil, errors.New("connection refused") }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		if _, err := grpcapi.NewClient(conn).Probe(t.Context(), "http://x"); !errors.Is(err, probe.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("CallerDeadline", func(t *testing.T) {
		// Storage tells shutdown apart from a bad file by the context error.
		c := newClient(t, proberFunc(func(ctx context.Context, _ string) (probe.Info, error) {
			<-ctx.Done()
			return probe.Info{}, ctx.Err()
		}))
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		if _, err := c.Probe(ctx, "http://x"); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", err)
		}
	})
}
