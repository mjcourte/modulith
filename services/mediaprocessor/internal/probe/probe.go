package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type probeResult struct {
	Format  formatInfo   `json:"format"`
	Streams []streamInfo `json:"streams"`
}

type formatInfo struct {
	Name     string `json:"format_name"`
	Duration string `json:"duration"`
	BitRate  string `json:"bit_rate"`
}

type streamInfo struct {
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FrameRate   string `json:"avg_frame_rate"`
	SampleRate  string `json:"sample_rate"`
	Channels    int    `json:"channels"`
	BitRate     string `json:"bit_rate"`
	Duration    string `json:"duration"`
	Disposition struct {
		AttachedPic int `json:"attached_pic"` // 1 for cover art in audio files
	} `json:"disposition"`
	SideData []struct {
		Rotation int `json:"rotation"`
	} `json:"side_data_list"`
}

// Prober reads a media file's metadata from a URL without downloading the
// whole file. Implemented by FFprobe; storage depends on this interface.
type Prober interface {
	Probe(ctx context.Context, url string) (Info, error)
}

// ErrNoMedia means the file has no audio or video streams.
var ErrNoMedia = errors.New("mediaprocessor: no audio or video streams")

// ErrUnavailable means a remote Prober couldn't be reached: the file wasn't
// probed, so callers should retry it later rather than count it as failed.
var ErrUnavailable = errors.New("mediaprocessor: unavailable")

// probeTimeout bounds one ffprobe run, including its range requests.
const probeTimeout = 30 * time.Second

// FFprobe is the Prober that runs the ffprobe binary.
type FFprobe struct {
	path string
}

// Compile-time check that *FFprobe satisfies Prober.
var _ Prober = (*FFprobe)(nil)

// NewFFprobe finds ffprobe on PATH, so a missing install fails at startup
// rather than on the first upload.
func NewFFprobe() (*FFprobe, error) {
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, fmt.Errorf("mediaprocessor: %w", err)
	}
	return &FFprobe{path: path}, nil
}

// Probe runs ffprobe on url, an http(s) URL such as a presigned S3 GET.
// ffprobe reads only what it needs, using range requests.
//
// ffprobe parses untrusted files, so it is confined: it gets an empty
// environment (never our S3 or database credentials) and may only open
// network URLs, never local files. The URL is a secret too (a presigned URL
// grants access), so it is kept out of the returned errors.
func (f *FFprobe) Probe(ctx context.Context, url string) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, f.path,
		"-v", "error",
		"-protocol_whitelist", "http,https,tls,tcp",
		"-probesize", "5000000", // bytes read to find the streams (ffprobe's default, made explicit)
		"-analyzeduration", "5000000", // microseconds of media analysed, likewise
		"-show_entries", "format=format_name,duration,bit_rate"+
			":stream=codec_type,codec_name,width,height,avg_frame_rate,sample_rate,channels,bit_rate,duration"+
			":stream_disposition=attached_pic:stream_side_data=rotation",
		"-of", "json",
		"--", url,
	)
	// Empty, not nil: a nil Env would inherit ours. (On Windows, Go adds the
	// SYSTEMROOT that networking needs.)
	cmd.Env = []string{}

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return Info{}, fmt.Errorf("ffprobe timed out or was canceled: %w", ctx.Err())
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			// ffprobe's messages name the input, so hide the URL.
			msg := strings.ReplaceAll(string(exitErr.Stderr), url, "<url>")
			return Info{}, fmt.Errorf("ffprobe failed: %w: %s", err, truncate(strings.TrimSpace(msg), 500))
		}
		return Info{}, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result probeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return Info{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	info := result.toInfo()
	if len(info.Video) == 0 && len(info.Audio) == 0 {
		return Info{}, ErrNoMedia
	}
	return info, nil
}

// truncate shortens s to at most n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Helpers for fields ffprobe represents as strings.
// parseSeconds turns ffprobe's seconds ("2.837292") into a Duration; 0 if
// unknown ("N/A", "").
func parseSeconds(s string) time.Duration {
	seconds, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	// Round, don't truncate: 2.837292 * 1e9 can come out as ...1999.9999.
	return time.Duration(math.Round(seconds * float64(time.Second)))
}

func atoi64(s string) int64 {
	a, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return a
}

func parseFraction(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return 0
	}
	af64, aerr := strconv.ParseFloat(a, 64)
	bf64, berr := strconv.ParseFloat(b, 64)
	if aerr != nil || berr != nil || bf64 == 0 {
		return 0
	}

	return af64 / bf64
}

func rotation(s streamInfo) int {
	for _, d := range s.SideData {
		if d.Rotation != 0 {
			return d.Rotation
		}
	}
	return 0
}

func (r probeResult) toInfo() Info {
	info := Info{
		Duration: parseSeconds(r.Format.Duration),
		BitRate:  atoi64(r.Format.BitRate),
		Format:   r.Format.Name,
	}

	maxDuration := info.Duration // or 0
	for _, s := range r.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition.AttachedPic == 1 {
				continue // cover art, not a real video track
			}
			s_rotation := rotation(s)
			info.Video = append(info.Video, VideoStream{
				FrameRate: parseFraction(s.FrameRate),
				Width:     s.Width,
				Height:    s.Height,
				Codec:     s.CodecName,
				BitRate:   atoi64(s.BitRate),
				Rotation:  s_rotation,
			})

		case "audio":
			info.Audio = append(info.Audio, AudioStream{
				SampleRate: int(atoi64(s.SampleRate)), // to int32
				Channels:   s.Channels,
				Codec:      s.CodecName,
				BitRate:    atoi64(s.BitRate),
			})
		}
		// do not compare duration from cover art
		maxDuration = max(maxDuration, parseSeconds(s.Duration))
	}

	if len(info.Video) > 0 {
		v := info.Video[0]
		info.DisplayWidth, info.DisplayHeight = v.Width, v.Height
		if v.Rotation%180 != 0 {
			info.DisplayWidth, info.DisplayHeight = v.Height, v.Width
		}
	}

	if info.Duration == 0 {
		info.Duration = maxDuration
	}

	return info
}
