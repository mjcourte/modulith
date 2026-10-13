// Package grpcapi serves a probe.Prober over gRPC (Server) and calls a remote
// one (Client), mapping errors both ways so callers see the same errors as
// in-process.
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	pb "gyoza/contracts/gen/go/gyoza/mediaprocessor/v1"
	"gyoza/services/mediaprocessor/internal/probe"
)

// errorDomain identifies this service in google.rpc.ErrorInfo details.
const errorDomain = "mediaprocessor.gyoza"

const reasonNoMedia = "NO_MEDIA"

// ---- Server

// Server exposes a Prober over gRPC. Register it with
// pb.RegisterMediaProcessorServiceServer.
type Server struct {
	pb.UnimplementedMediaProcessorServiceServer
	prober probe.Prober
}

func NewServer(p probe.Prober) *Server { return &Server{prober: p} }

func (s *Server) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	info, err := s.prober.Probe(ctx, req.GetUrl())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ProbeResponse{Info: infoToProto(info)}, nil
}

// ---- Client

// Client is a Prober calling a remote MediaProcessorService.
type Client struct {
	rpc pb.MediaProcessorServiceClient
}

var _ probe.Prober = (*Client)(nil)

func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{rpc: pb.NewMediaProcessorServiceClient(conn)}
}

func (c *Client) Probe(ctx context.Context, url string) (probe.Info, error) {
	resp, err := c.rpc.Probe(ctx, &pb.ProbeRequest{Url: url})
	if err != nil {
		return probe.Info{}, fromStatus(err)
	}
	return infoFromProto(resp.GetInfo()), nil
}

// ---- Errors

// toStatus turns a Probe error into a gRPC status. Unlike storage, other
// errors keep their message: Probe's errors are ffprobe's output with the URL
// removed, and storage records them on the file to explain why it failed.
func toStatus(err error) error {
	switch {
	case errors.Is(err, probe.ErrNoMedia):
		st := status.New(codes.InvalidArgument, err.Error())
		if withInfo, detailErr := st.WithDetails(&errdetails.ErrorInfo{Domain: errorDomain, Reason: reasonNoMedia}); detailErr == nil {
			st = withInfo
		}
		return st.Err()
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Unknown, err.Error())
	}
}

// remoteError keeps the server's message and matches the original error
// with errors.Is.
type remoteError struct {
	msg string
	err error
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.err }

// fromStatus turns a gRPC error back into the error Probe returned.
func fromStatus(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err // not from gRPC
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == errorDomain && info.GetReason() == reasonNoMedia {
			return &remoteError{msg: st.Message(), err: probe.ErrNoMedia}
		}
	}
	switch st.Code() {
	case codes.Canceled:
		return &remoteError{msg: st.Message(), err: errors.Join(context.Canceled, err)}
	case codes.DeadlineExceeded:
		return &remoteError{msg: st.Message(), err: errors.Join(context.DeadlineExceeded, err)}
	case codes.Unknown:
		return errors.New(st.Message()) // ffprobe's message, as in-process
	case codes.Unavailable:
		return &remoteError{msg: st.Message(), err: errors.Join(probe.ErrUnavailable, err)}
	default:
		return err
	}
}

// ---- Conversions

func infoToProto(i probe.Info) *pb.MediaInfo {
	p := &pb.MediaInfo{
		Duration:      durationpb.New(i.Duration),
		DisplayWidth:  int32(i.DisplayWidth),
		DisplayHeight: int32(i.DisplayHeight),
		BitRate:       i.BitRate,
		Format:        i.Format,
	}
	for _, v := range i.Video {
		p.Video = append(p.Video, &pb.VideoStream{
			FrameRate: v.FrameRate,
			Width:     int32(v.Width),
			Height:    int32(v.Height),
			Rotation:  int32(v.Rotation),
			Codec:     v.Codec,
			BitRate:   v.BitRate,
		})
	}
	for _, a := range i.Audio {
		p.Audio = append(p.Audio, &pb.AudioStream{
			SampleRate: int32(a.SampleRate),
			Channels:   int32(a.Channels),
			Codec:      a.Codec,
			BitRate:    a.BitRate,
		})
	}
	return p
}

func infoFromProto(p *pb.MediaInfo) probe.Info {
	i := probe.Info{
		Duration:      p.GetDuration().AsDuration(),
		DisplayWidth:  int(p.GetDisplayWidth()),
		DisplayHeight: int(p.GetDisplayHeight()),
		BitRate:       p.GetBitRate(),
		Format:        p.GetFormat(),
	}
	for _, v := range p.GetVideo() {
		i.Video = append(i.Video, probe.VideoStream{
			FrameRate: v.GetFrameRate(),
			Width:     int(v.GetWidth()),
			Height:    int(v.GetHeight()),
			Rotation:  int(v.GetRotation()),
			Codec:     v.GetCodec(),
			BitRate:   v.GetBitRate(),
		})
	}
	for _, a := range p.GetAudio() {
		i.Audio = append(i.Audio, probe.AudioStream{
			SampleRate: int(a.GetSampleRate()),
			Channels:   int(a.GetChannels()),
			Codec:      a.GetCodec(),
			BitRate:    a.GetBitRate(),
		})
	}
	return i
}
