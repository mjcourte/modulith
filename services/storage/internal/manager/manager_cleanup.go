package manager

// The cleanup job: everything that should disappear on its own. Run by
// RunCleanup on a timer in every binary that hosts the Manager. Each step is
// idempotent and works through old items first, so several replicas running
// it at once only repeat each other's work, and a run that dies part-way is
// finished by the next one.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"gyoza/platform/telemetry"

	"gyoza/services/storage/internal/ledger"
	"gyoza/services/storage/internal/snapshots"
)

const (
	// cleanupBatch is how many items each step fetches at a time.
	cleanupBatch = 100
	// clockSkew is the margin between replicas' clocks the job allows for:
	// an expired upload is only removed this long after it expired, and a
	// trashed item can't be restored for this long before it's purged.
	clockSkew = 5 * time.Minute
)

// CleanupReport says what one cleanup run removed.
type CleanupReport struct {
	ExpiredUploads   int // pending sessions past expiry, with their files
	CompletedUploads int // sessions completed longer ago than the retention period
	Assets           int // trashed assets, with every version's file
	Folders          int // trashed folders
	Objects          int // other deleted ledger objects, with their files
	ExpiredSnapshots int // abandoned snapshot uploads, with their files
	OldSnapshots     int // snapshots beyond each project's newest SnapshotKeep, with their files
}

// Cleanup removes, as of now:
//   - upload sessions that expired before completing, with whatever file the
//     browser put in S3 and any ledger object recorded for it;
//   - completed sessions older than the trash retention period (a retried
//     CompleteUpload is long over by then);
//   - trash older than the retention period: assets with all their versions'
//     files and ledger objects, then folders;
//   - any other ledger object deleted longer ago than the retention period
//     (e.g. a deleted project's exports), with its file.
//   - snapshot uploads abandoned before completing, and snapshots beyond each
//     project's newest SnapshotKeep, with their files and ledger objects.
//
// It stops at the first failure and returns what it did so far; the next run
// retries from there.
func (m *Manager) Cleanup(ctx context.Context, now time.Time) (CleanupReport, error) {
	var r CleanupReport
	var err error
	cutoff := now.Add(-m.cfg.TrashRetention)
	if r.ExpiredUploads, err = m.cleanExpiredUploads(ctx, now.Add(-clockSkew)); err != nil {
		return r, fmt.Errorf("expired uploads: %w", err)
	}
	if r.CompletedUploads, err = drain(ctx, func() (int, error) {
		return m.store.DeleteCompletedUploads(ctx, cutoff, cleanupBatch)
	}); err != nil {
		return r, fmt.Errorf("completed uploads: %w", err)
	}
	if r.Assets, err = m.purgeAssets(ctx, cutoff); err != nil {
		return r, fmt.Errorf("purge assets: %w", err)
	}
	// After assets, so the assets in purged folders are mostly gone already.
	if r.Folders, err = drain(ctx, func() (int, error) {
		return m.store.PurgeFolders(ctx, cutoff, cleanupBatch)
	}); err != nil {
		return r, fmt.Errorf("purge folders: %w", err)
	}
	if r.Objects, err = m.purgeLedgerObjects(ctx, cutoff); err != nil {
		return r, fmt.Errorf("purge ledger objects: %w", err)
	}
	if r.ExpiredSnapshots, err = m.cleanSnapshots(ctx, func() ([]snapshots.Snapshot, error) {
		return m.snaps.ListExpiredPending(ctx, now.Add(-clockSkew), cleanupBatch)
	}); err != nil {
		return r, fmt.Errorf("expired snapshots: %w", err)
	}
	if r.OldSnapshots, err = m.cleanSnapshots(ctx, func() ([]snapshots.Snapshot, error) {
		return m.snaps.ListBeyondKeep(ctx, m.cfg.SnapshotKeep, cleanupBatch)
	}); err != nil {
		return r, fmt.Errorf("old snapshots: %w", err)
	}
	return r, nil
}

// cleanSnapshots drops every snapshot list returns, batch by batch: its
// object, ledger row and record. Old snapshots go at once rather than
// through the trash: they're past the revert window by definition.
func (m *Manager) cleanSnapshots(ctx context.Context, list func() ([]snapshots.Snapshot, error)) (int, error) {
	return drain(ctx, func() (int, error) {
		batch, err := list()
		if err != nil {
			return 0, err
		}
		for i, s := range batch {
			if err := m.dropSnapshot(ctx, s); err != nil {
				return i, err
			}
		}
		return len(batch), nil
	})
}

// drain calls batch until it does less than a full batch, adding up the counts.
func drain(ctx context.Context, batch func() (int, error)) (int, error) {
	total := 0
	for {
		n, err := batch()
		total += n
		if err != nil || n < cleanupBatch {
			return total, err
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func (m *Manager) cleanExpiredUploads(ctx context.Context, before time.Time) (int, error) {
	return drain(ctx, func() (int, error) {
		expired, err := m.store.ListExpiredUploads(ctx, before, cleanupBatch)
		if err != nil {
			return 0, err
		}
		for i, u := range expired {
			// The file first and the session last: if anything fails, the
			// session is still listed next time and the rest is retried.
			if err := m.blobs.Delete(ctx, u.Bucket, u.Key); err != nil {
				return i, fmt.Errorf("delete file of upload %s: %w", u.ID, err)
			}
			// A completion that got as far as the ledger, then found the
			// session expired, left an object for a file that's now gone.
			if err := m.purgeObject(ctx, u.ObjectID); err != nil {
				return i, err
			}
			if err := m.store.DeleteUpload(ctx, u.ID); err != nil {
				return i, err
			}
		}
		return len(expired), nil
	})
}

func (m *Manager) purgeAssets(ctx context.Context, before time.Time) (int, error) {
	return drain(ctx, func() (int, error) {
		assets, err := m.store.ListAssetsTrashedBefore(ctx, before, cleanupBatch)
		if err != nil {
			return 0, err
		}
		for i, a := range assets {
			versions, err := m.store.ListVersions(ctx, a.ID)
			if err != nil {
				return i, err
			}
			// Files and ledger objects first, the asset last: a retry finds
			// the asset again and skips what's already gone.
			for _, v := range versions {
				if err := m.deleteObjectAndFile(ctx, v.ObjectID); err != nil {
					return i, fmt.Errorf("asset %s version %d: %w", a.ID, v.Number, err)
				}
			}
			if err := m.store.PurgeAsset(ctx, a.ID); err != nil {
				return i, err
			}
		}
		return len(assets), nil
	})
}

func (m *Manager) purgeLedgerObjects(ctx context.Context, before time.Time) (int, error) {
	return drain(ctx, func() (int, error) {
		objs, err := m.ledger.ListDeleted(ctx, before, cleanupBatch)
		if err != nil {
			return 0, err
		}
		for i, obj := range objs {
			if err := m.blobs.Delete(ctx, obj.Bucket, obj.Key); err != nil {
				return i, fmt.Errorf("delete file of object %s: %w", obj.ID, err)
			}
			if err := m.ledger.Purge(ctx, obj.ID); err != nil {
				return i, err
			}
		}
		return len(objs), nil
	})
}

// deleteObjectAndFile deletes a ledger object's file from S3, then the object
// itself. An object that's already gone is skipped.
func (m *Manager) deleteObjectAndFile(ctx context.Context, objectID string) error {
	obj, err := m.ledger.Get(ctx, objectID)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.blobs.Delete(ctx, obj.Bucket, obj.Key); err != nil {
		return fmt.Errorf("delete file of object %s: %w", objectID, err)
	}
	return m.purgeObject(ctx, objectID)
}

// purgeObject removes a ledger object for good, whether or not it was marked
// deleted yet. Missing is fine.
func (m *Manager) purgeObject(ctx context.Context, objectID string) error {
	if err := m.ledger.MarkDeleted(ctx, objectID); errors.Is(err, ledger.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return m.ledger.Purge(ctx, objectID)
}

// restorable reports whether something trashed at deletedAt may still come
// back. Once its retention has run out (less a margin for clock skew) it
// belongs to the cleanup job, which may already be deleting its files, so a
// restore and a purge never overlap.
func (m *Manager) restorable(deletedAt *time.Time) bool {
	return deletedAt == nil || m.now().Sub(*deletedAt) < m.cfg.TrashRetention-clockSkew
}

var tracer = otel.Tracer("gyoza/services/storage/internal/manager")

// RunCleanup runs Cleanup now and then every interval until ctx is done,
// logging what each run did. Run it in its own goroutine.
func (m *Manager) RunCleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Each run is a trace of its own: its queries and S3 calls are only
		// traced inside one.
		rctx, span := tracer.Start(ctx, "storage.cleanup", trace.WithNewRoot())
		r, err := m.Cleanup(rctx, m.now())
		span.SetAttributes(attribute.String("gyoza.done", fmt.Sprintf("%+v", r)))
		switch {
		case ctx.Err() != nil:
			span.End()
			return // shutting down: an error now is just the cancellation
		case err != nil:
			slog.ErrorContext(rctx, "storage: cleanup failed", "err", err, "done", r)
		case r != CleanupReport{}:
			slog.InfoContext(rctx, "storage: cleanup", "done", r)
		}
		telemetry.End(span, &err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
