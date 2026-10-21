package probe_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/mediaprocessor/internal/probe"
)

// newFFprobe returns the real prober, or skips the test without ffprobe on PATH.
func newFFprobe(t *testing.T) *probe.FFprobe {
	t.Helper()
	p, err := probe.NewFFprobe()
	if err != nil {
		t.Skipf("ffprobe not available: %v", err)
	}
	return p
}

// TestProbeOverHTTP probes a real file the way storage does: over HTTP, as a
// presigned S3 URL would serve it (http.FileServer answers range requests).
func TestProbeOverHTTP(t *testing.T) {
	p := newFFprobe(t)
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)

	info, err := p.Probe(t.Context(), srv.URL+"/tiny_rotated.mp4")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// 64x48 stored, rotated 90: shown 48x64.
	if info.DisplayWidth != 48 || info.DisplayHeight != 64 {
		t.Errorf("display size = %dx%d, want 48x64", info.DisplayWidth, info.DisplayHeight)
	}
	if info.Duration != time.Second {
		t.Errorf("Duration = %s, want 1s", info.Duration)
	}
	if len(info.Video) != 1 || info.Video[0].Codec != "h264" || info.Video[0].Rotation != 90 {
		t.Errorf("Video = %+v, want one h264 stream rotated 90", info.Video)
	}
	if len(info.Audio) != 1 || info.Audio[0].Codec != "aac" || info.Audio[0].SampleRate != 8000 {
		t.Errorf("Audio = %+v, want one 8 kHz aac stream", info.Audio)
	}
}

// TestProbeRefusesLocalFiles: a crafted input must not make ffprobe read
// local files (.env holds credentials).
func TestProbeRefusesLocalFiles(t *testing.T) {
	p := newFFprobe(t)
	for _, url := range []string{"testdata/tiny_rotated.mp4", "file:testdata/tiny_rotated.mp4"} {
		if _, err := p.Probe(t.Context(), url); err == nil {
			t.Errorf("Probe(%q) succeeded; local files must be refused", url)
		}
	}
}

// TestProbeErrorHidesURL: a presigned URL grants access, so it must not
// leak into errors (and from there into logs or the database).
func TestProbeErrorHidesURL(t *testing.T) {
	p := newFFprobe(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	url := srv.URL + "/missing.mp4?X-Amz-Signature=secret"
	_, err := p.Probe(t.Context(), url)
	if err == nil {
		t.Fatal("Probe of a missing file succeeded")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error contains the URL: %v", err)
	}
}
