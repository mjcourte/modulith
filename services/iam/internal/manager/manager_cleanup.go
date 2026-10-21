package manager

// The cleanup loop: finishes deletions and clones that were cut off part-way,
// and removes expired invites. Like storage's, it runs inside whichever binary hosts the
// Manager and stays off Service.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/mjcourte/modulith/platform/telemetry"
)

// CleanupReport says what one Cleanup run did.
type CleanupReport struct {
	ExpiredInvites int
	ExpiredShares  int // pending shares unclaimed in time
	// Deletions finished (or found already done) this run.
	Users, Orgs, Projects int
	// Clones cut off part-way: finished, or abandoned (deleted) because their
	// source is gone or they kept failing for a day.
	ClonesFinished, ClonesAbandoned int
}

// Cleanup removes expired invites and finishes up to one batch each of the
// users, orgs and projects marked deleting, longest-waiting first. One
// failing deletion doesn't stop the others: the errors are joined, and the
// failed ones stay marked for the next run.
func (m *Manager) Cleanup(ctx context.Context) (CleanupReport, error) {
	var r CleanupReport
	var errs []error

	for {
		n, err := m.store.DeleteExpiredInvites(ctx, m.now(), cascadeBatch)
		r.ExpiredInvites += n
		if err != nil {
			errs = append(errs, err)
		}
		if err != nil || n < cascadeBatch {
			break
		}
	}
	for {
		n, err := m.store.DeleteExpiredPendingShares(ctx, m.now(), cascadeBatch)
		r.ExpiredShares += n
		if err != nil {
			errs = append(errs, err)
		}
		if err != nil || n < cascadeBatch {
			break
		}
	}

	// Users first: finishing one can delete an org (its last member left).
	if users, err := m.store.ListDeletingUsers(ctx, cascadeBatch); err != nil {
		errs = append(errs, err)
	} else {
		for _, u := range users {
			if err := m.finishUser(ctx, u.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			r.Users++
		}
	}
	if orgs, err := m.store.ListDeletingOrgs(ctx, cascadeBatch); err != nil {
		errs = append(errs, err)
	} else {
		for _, o := range orgs {
			if err := m.deleteOrg(ctx, o.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			r.Orgs++
		}
	}
	if projects, err := m.store.ListDeletingProjects(ctx, cascadeBatch); err != nil {
		errs = append(errs, err)
	} else {
		for _, p := range projects {
			if err := m.deleteProject(ctx, p.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			r.Projects++
		}
	}
	// After deletions, so a clone of a project just deleted is abandoned.
	var cloneErrs []error
	r.ClonesFinished, r.ClonesAbandoned, cloneErrs = m.cleanupClones(ctx)
	errs = append(errs, cloneErrs...)
	return r, errors.Join(errs...)
}

var tracer = otel.Tracer("modulith/services/iam/internal/manager")

// RunCleanup runs Cleanup now and then every interval until ctx is done,
// logging what each run did. Run it in its own goroutine.
func (m *Manager) RunCleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Each run is a trace of its own: its queries and calls to storage
		// are only traced inside one.
		rctx, span := tracer.Start(ctx, "iam.cleanup", trace.WithNewRoot())
		r, err := m.Cleanup(rctx)
		span.SetAttributes(attribute.String("modulith.done", fmt.Sprintf("%+v", r)))
		switch {
		case ctx.Err() != nil:
			span.End()
			return // shutting down: an error now is just the cancellation
		case err != nil:
			slog.ErrorContext(rctx, "iam: cleanup failed", "err", err, "done", r)
		case r != CleanupReport{}:
			slog.InfoContext(rctx, "iam: cleanup", "done", r)
		}
		telemetry.End(span, &err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
