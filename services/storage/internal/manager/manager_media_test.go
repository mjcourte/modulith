package manager_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/mediaprocessor"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	sm "github.com/mjcourte/modulith/services/storage/internal/manager"
)

// proberFunc is a mediaprocessor.Prober made from a function.
type proberFunc func(ctx context.Context, url string) (mediaprocessor.Info, error)

func (f proberFunc) Probe(ctx context.Context, url string) (mediaprocessor.Info, error) {
	return f(ctx, url)
}

// uploadMedia uploads a file of contentType to acme's library and returns
// its ledger object ID and bytes.
func uploadMedia(t *testing.T, tm testManager, contentType string) (string, []byte) {
	t.Helper()
	data := fileOf(contentType, 1024)
	objectID, _ := uploadData(t, tm, contentType, data)
	return objectID, data
}

// uploadData uploads data as a new asset of acme's and returns its ledger
// object ID and the asset.
func uploadData(t *testing.T, tm testManager, contentType string, data []byte) (string, dam.Asset) {
	t.Helper()
	req := sm.UploadRequest{Owner: acme, Name: "file", ContentType: contentType, SizeBytes: int64(len(data))}
	ticket, err := tm.BeginUpload(t.Context(), "member", req)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if err := tm.blobs.PutSigned(ticket.URL, contentType, data); err != nil {
		t.Fatalf("browser upload: %v", err)
	}
	a, err := tm.CompleteUpload(t.Context(), "member", ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload(%s): %v", contentType, err)
	}
	return mustListVersions(t, tm.store, a.ID)[0].ObjectID, a
}

var phoneClip = mediaprocessor.Info{
	Duration:      2837292 * time.Microsecond,
	DisplayWidth:  1080,
	DisplayHeight: 1920,
	Format:        "mov,mp4,m4a,3gp,3g2,mj2",
	Video:         []mediaprocessor.VideoStream{{Codec: "hevc", Width: 1920, Height: 1080, Rotation: -90}},
	Audio:         []mediaprocessor.AudioStream{{Codec: "aac", SampleRate: 48000, Channels: 2}},
}

func TestCompleteUploadQueuesMedia(t *testing.T) {
	tm := newTestManager(t)
	video, _ := uploadMedia(t, tm, "video/mp4")
	info, err := tm.ledger.GetMediaInfo(t.Context(), video)
	if err != nil || info.Status != ledger.MediaPending {
		t.Errorf("video's media info = %+v, %v; want pending", info, err)
	}
}

func TestProbeMediaRecordsResult(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	objectID, data := uploadMedia(t, tm, "video/mp4")

	prober := proberFunc(func(ctx context.Context, url string) (mediaprocessor.Info, error) {
		// The URL must serve this very file, as S3 would to ffprobe.
		got, _, err := tm.blobs.GetSigned(url)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("probe URL serves %d bytes, %v; want the uploaded file", len(got), err)
		}
		return phoneClip, nil
	})
	if n, err := tm.ProbeMedia(ctx, prober); n != 1 || err != nil {
		t.Fatalf("ProbeMedia = %d, %v; want 1, nil", n, err)
	}

	info, err := tm.ledger.GetMediaInfo(ctx, objectID)
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if info.Status != ledger.MediaReady || info.Width != 1080 || info.Height != 1920 || info.Duration != phoneClip.Duration {
		t.Errorf("media info = %+v; want ready, 1080x1920, %s", info, phoneClip.Duration)
	}
	var details mediaprocessor.Info
	if err := json.Unmarshal(info.Details, &details); err != nil || !reflect.DeepEqual(details, phoneClip) {
		t.Errorf("Details = %s (%v); want the whole probe result", info.Details, err)
	}

	if n, err := tm.ProbeMedia(ctx, prober); n != 0 || err != nil {
		t.Errorf("second ProbeMedia = %d, %v; want nothing left", n, err)
	}
}

func TestProbeMediaRetriesThenFails(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	objectID, _ := uploadMedia(t, tm, "video/mp4")
	broken := proberFunc(func(context.Context, string) (mediaprocessor.Info, error) {
		return mediaprocessor.Info{}, errors.New("moov atom not found")
	})

	for attempt := 1; attempt <= sm.MaxProbeAttempts; attempt++ {
		if _, err := tm.ProbeMedia(ctx, broken); err != nil {
			t.Fatalf("ProbeMedia: %v (a failed probe is recorded, not returned)", err)
		}
		info, _ := tm.ledger.GetMediaInfo(ctx, objectID)
		want := ledger.MediaPending
		if attempt == sm.MaxProbeAttempts {
			want = ledger.MediaFailed
		}
		if info.Status != want || info.Attempts != attempt || info.Error != "moov atom not found" {
			t.Errorf("after attempt %d: %+v; want %s, %d attempts, the error kept", attempt, info, want, attempt)
		}
	}
	if n, _ := tm.ProbeMedia(ctx, broken); n != 0 {
		t.Errorf("failed file probed again (%d)", n)
	}
}

// TestProbeMediaShutdown: a probe cut off by shutdown isn't the file's
// fault, so it mustn't use up an attempt.
func TestProbeMediaShutdown(t *testing.T) {
	tm := newTestManager(t)
	objectID, _ := uploadMedia(t, tm, "video/mp4")
	ctx, cancel := context.WithCancel(t.Context())
	interrupted := proberFunc(func(ctx context.Context, _ string) (mediaprocessor.Info, error) {
		cancel()
		return mediaprocessor.Info{}, context.Canceled
	})

	if _, err := tm.ProbeMedia(ctx, interrupted); !errors.Is(err, context.Canceled) {
		t.Errorf("ProbeMedia = %v, want context.Canceled", err)
	}
	info, _ := tm.ledger.GetMediaInfo(t.Context(), objectID)
	if info.Status != ledger.MediaPending || info.Attempts != 0 {
		t.Errorf("after shutdown: %+v; want still pending, no attempts used", info)
	}
}

// TestProbeMediaUnavailable: a mediaprocessor that can't be reached isn't
// the file's fault either, so files wait for it instead of failing.
func TestProbeMediaUnavailable(t *testing.T) {
	tm := newTestManager(t)
	objectID, _ := uploadMedia(t, tm, "video/mp4")
	down := proberFunc(func(context.Context, string) (mediaprocessor.Info, error) {
		return mediaprocessor.Info{}, fmt.Errorf("%w: connection refused", mediaprocessor.ErrUnavailable)
	})

	for range sm.MaxProbeAttempts {
		if _, err := tm.ProbeMedia(t.Context(), down); !errors.Is(err, mediaprocessor.ErrUnavailable) {
			t.Fatalf("ProbeMedia = %v, want ErrUnavailable", err)
		}
	}
	info, _ := tm.ledger.GetMediaInfo(t.Context(), objectID)
	if info.Status != ledger.MediaPending || info.Attempts != 0 {
		t.Errorf("after outage: %+v; want still pending, no attempts used", info)
	}
}
