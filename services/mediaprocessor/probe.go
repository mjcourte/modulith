// this calls ffprobe
// some of this could move to platform
// and in media processor we would need the data models for our types

package mediaprocessor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

type ProbeResult struct {
	Format  FormatInfo   `json:"format"`
	Streams []StreamInfo `json:"streams"`
}

type FormatInfo struct {
	Name     string `json:"format_name"`
	Duration string `json:"duration"`
	Size     string `json:"size"`
	BitRate  string `json:"bit_rate"`
}

type StreamInfo struct {
	Index       int    `json:"index"`
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pix_fmt"`
	FrameRate   string `json:"avg_frame_rate"`
	SampleRate  string `json:"sample_rate"`
	Channels    int    `json:"channels"`
	BitRate     string `json:"bit_rate"`
}

func Inspect(ctx context.Context, path string) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-show_format",
		"-show_streams",
		"-of", "json",
		"--", path,
	)

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ffprobe timed out or was canceled: %w", ctx.Err())
		}
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result ProbeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("decode ffprobe output: %w", err)
	}

	return &result, nil
}

// Helpers for fields ffprobe represents as strings.
func (f FormatInfo) DurationSeconds() (float64, error) {
	return strconv.ParseFloat(f.Duration, 64)
}

func (f FormatInfo) SizeBytes() (int64, error) {
	return strconv.ParseInt(f.Size, 10, 64)
}
