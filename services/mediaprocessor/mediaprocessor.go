// Package mediaprocessor probes media files with ffprobe.
//
// This file is the public surface; the code is under internal/: probe (ffprobe
// and the Info it returns) and grpcapi (the gRPC adapters). The names below
// are aliases (type X = Y), so values flow between them without conversion.
package mediaprocessor

import (
	"google.golang.org/grpc"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/mediaprocessor/v1"
	"github.com/mjcourte/modulith/services/mediaprocessor/internal/grpcapi"
	"github.com/mjcourte/modulith/services/mediaprocessor/internal/probe"
)

type (
	Prober      = probe.Prober
	FFprobe     = probe.FFprobe
	Info        = probe.Info
	VideoStream = probe.VideoStream
	AudioStream = probe.AudioStream
)

// ErrNoMedia means the file has no audio or video streams.
var ErrNoMedia = probe.ErrNoMedia

// ErrUnavailable means a remote mediaprocessor couldn't be reached; retry later.
var ErrUnavailable = probe.ErrUnavailable

// NewFFprobe finds ffprobe on PATH, so a missing install fails at startup.
func NewFFprobe() (*FFprobe, error) { return probe.NewFFprobe() }

// RegisterGRPC serves p as MediaProcessorService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, p Prober) {
	pb.RegisterMediaProcessorServiceServer(r, grpcapi.NewServer(p))
}

// NewGRPCClient is a Prober calling a remote mediaprocessor over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Prober { return grpcapi.NewClient(conn) }
