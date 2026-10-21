package manager

// The media probe job: fills in the media info (dimensions, duration,
// codecs) of uploaded video and audio, so it can be read without fetching
// the file. CompleteUpload queues each one (ledger.MarkMediaPending); the job
// presigns a short-lived GET and hands it to a mediaprocessor.Prober, which
// never sees S3 credentials. Like cleanup, it runs in whichever binary hosts
// the Manager, and several replicas running it at once only repeat work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mjcourte/modulith/services/mediaprocessor"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

const (
	// probeBatch is how many pending files one ProbeMedia call takes.
	probeBatch = 10
	// maxProbeAttempts is how often a file is tried before it's marked failed.
	maxProbeAttempts = 3
	// probeURLTTL is how long the URL handed to the prober works: longer than
	// one ffprobe run, short enough to be useless if it leaks.
	probeURLTTL = 2 * time.Minute
)

// needsProbe reports whether files of contentType get media info from the
// probe job. Images are left out: ffprobe sees them as one-frame videos.
func needsProbe(contentType string) bool {
	c := dam.CategoryOf(contentType)
	return c == dam.CategoryVideo || c == dam.CategoryAudio
}

// ProbeMedia probes up to probeBatch pending files with p and records each
// result. A failed probe stays pending, at the back of the queue, until it
// has failed maxProbeAttempts times; then it's marked failed. It returns how
// many files it handled, and stops at the first error that isn't the probe's.
func (m *Manager) ProbeMedia(ctx context.Context, p mediaprocessor.Prober) (int, error) {
	pending, err := m.ledger.ListPendingMedia(ctx, probeBatch)
	if err != nil {
		return 0, err
	}
	for i, info := range pending {
		if err := m.probeOne(ctx, p, info); err != nil {
			return i, fmt.Errorf("probe object %s: %w", info.ObjectID, err)
		}
	}
	return len(pending), nil
}

// probeOne probes one pending file and records the outcome. Only failures
// to read or record are returned; a failed probe is recorded instead.
func (m *Manager) probeOne(ctx context.Context, p mediaprocessor.Prober, pending ledger.MediaInfo) error {
	obj, err := m.ledger.Get(ctx, pending.ObjectID)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil // purged since it was listed
	}
	if err != nil {
		return err
	}
	url, err := m.blobs.PresignGet(ctx, obj.Bucket, obj.Key, probeURLTTL, "")
	if err != nil {
		return err
	}

	info, probeErr := p.Probe(ctx, url)
	if ctx.Err() != nil {
		return ctx.Err() // shutting down: not the file's fault, so not an attempt
	}
	if errors.Is(probeErr, mediaprocessor.ErrUnavailable) {
		return probeErr // likewise: the file waits until mediaprocessor is back
	}
	if probeErr != nil {
		failed := pending
		failed.Attempts++
		failed.Error = probeErr.Error()
		if failed.Attempts >= maxProbeAttempts {
			failed.Status = ledger.MediaFailed
		}
		slog.Warn("storage: media probe failed", "object", obj.ID, "attempt", failed.Attempts, "err", probeErr)
		return m.ledger.SetMediaInfo(ctx, failed)
	}

	details, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return m.ledger.SetMediaInfo(ctx, ledger.MediaInfo{
		ObjectID: obj.ID,
		Status:   ledger.MediaReady,
		Width:    info.DisplayWidth,
		Height:   info.DisplayHeight,
		Duration: info.Duration,
		Details:  details,
		Attempts: pending.Attempts,
	})
}

// RunMediaProbe probes pending files now and then every interval until ctx
// is done, working through the whole queue each time. Run it in its own
// goroutine.
func (m *Manager) RunMediaProbe(ctx context.Context, p mediaprocessor.Prober, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Full batches mean more may be waiting. Failures stay pending, so
		// the queue drains only as attempts run out; that bounds this loop.
		for {
			n, err := m.ProbeMedia(ctx, p)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				slog.Error("storage: media probe failed", "err", err)
			}
			if err != nil || n < probeBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
