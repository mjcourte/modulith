package manager

// Cloning: a new project with a copy of another's files. A clone is a job of
// several steps, so the project starts as ProjectCloning (hidden, granting
// nothing) and becomes ready at the end. Each step can be repeated, so the
// cleanup loop finishes clones cut off part-way, or abandons them.
//
//  1. create the project row (ProjectCloning, ClonedFrom the source)
//  2. storage copies the files (CopyProjectFiles: resumable, idempotent)
//  3. documents copies the document and rewrites its refs (CloneDocument)
//  4. mark it ready

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/mjcourte/modulith/platform/telemetry"
	"github.com/mjcourte/modulith/services/iam/internal/directory"
	"github.com/mjcourte/modulith/services/storage"
)

const (
	// cloneResumeAfter: the cleanup loop leaves younger clones alone, as
	// their CloneProject call may still be copying.
	cloneResumeAfter = 5 * time.Minute
	// cloneAbandonAfter: a clone still not ready this long after it started
	// is deleted rather than retried again.
	cloneAbandonAfter = 24 * time.Hour
)

// CloneProject makes a new project from sourceID, owned by target, with a
// copy of its files. The actor must be able to read the source (modulith
// templates included) and create projects for target: their own library or
// their org. An org-locked source can only be cloned into its own org, and
// the clone is locked too, so cloning can't get around the lock. The clone
// gets the source's name; shares aren't copied.
//
// If copying fails part-way, the clone is returned with the error: it stays
// ProjectCloning (hidden) and the cleanup loop finishes or abandons it.
func (m *Manager) CloneProject(ctx context.Context, actorID, sourceID string, target storage.Owner) (directory.Project, error) {
	src, _, err := m.access(ctx, actorID, sourceID, directory.AccessViewer)
	if err != nil {
		return directory.Project{}, err
	}
	if err := m.canCreateFor(ctx, actorID, target); err != nil {
		return directory.Project{}, err
	}
	if src.OrgLocked && target != src.Owner {
		return directory.Project{}, fmt.Errorf("%w: this project is locked to its org: it can only be cloned there", ErrForbidden)
	}
	clone, err := m.store.CreateProject(ctx, directory.Project{
		Owner: target, Name: src.Name, State: directory.ProjectCloning, ClonedFrom: src.ID,
		OrgLocked: src.OrgLocked, CreatedBy: actorID,
	})
	if err != nil {
		return directory.Project{}, err
	}
	return m.finishClone(ctx, clone)
}

// finishClone runs a clone's remaining steps; each can be repeated.
func (m *Manager) finishClone(ctx context.Context, clone directory.Project) (_ directory.Project, err error) {
	ctx, span := tracer.Start(ctx, "iam.finish_clone", trace.WithAttributes(attribute.String("modulith.project_id", clone.ID), attribute.String("modulith.cloned_from", clone.ClonedFrom)))
	defer telemetry.End(span, &err)
	if _, err := m.storage.CopyProjectFiles(ctx, clone.CreatedBy, clone.ClonedFrom, clone.ID); err != nil {
		return clone, fmt.Errorf("copy files of %s into clone %s: %w", clone.ClonedFrom, clone.ID, err)
	}
	if err := m.docs.CloneDocument(ctx, clone.CreatedBy, clone.ClonedFrom, clone.ID); err != nil {
		return clone, fmt.Errorf("copy the document of %s into clone %s: %w", clone.ClonedFrom, clone.ID, err)
	}
	return m.store.MarkProjectReady(ctx, clone.ID)
}

// sourceGone reports whether a clone's source can no longer be copied: it's
// been deleted or is being deleted.
func (m *Manager) sourceGone(ctx context.Context, clone directory.Project) (bool, error) {
	src, err := m.store.GetProject(ctx, clone.ClonedFrom)
	if errors.Is(err, directory.ErrProjectNotFound) {
		return true, nil
	}
	return err == nil && src.DeletingAt != nil, err
}

// cleanupClones finishes clones cut off part-way, and abandons (deletes)
// those whose source is gone or that have been retried for too long.
func (m *Manager) cleanupClones(ctx context.Context) (finished, abandoned int, errs []error) {
	clones, err := m.store.ListCloningProjects(ctx, m.now().Add(-cloneResumeAfter), cascadeBatch)
	if err != nil {
		return 0, 0, []error{err}
	}
	for _, c := range clones {
		gone, err := m.sourceGone(ctx, c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if gone || m.now().Sub(c.CreatedAt) > cloneAbandonAfter {
			if err := m.deleteProject(ctx, c.ID); err != nil {
				errs = append(errs, fmt.Errorf("abandon clone %s: %w", c.ID, err))
				continue
			}
			abandoned++
			continue
		}
		if _, err := m.finishClone(ctx, c); err != nil {
			errs = append(errs, err)
			continue
		}
		finished++
	}
	return finished, abandoned, errs
}
