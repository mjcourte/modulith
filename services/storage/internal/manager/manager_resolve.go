package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// ResolvedRef is what a document's pinned ref points at.
type ResolvedRef struct {
	Asset    dam.Asset // DeletedAt is set if it's in the trash: the UI marks it
	Version  dam.Version
	Download Download
}

// ResolveRef is how a document reads the files it uses: the asset, the
// pinned version and a short-lived link to its bytes. The actor must be able
// to read projectID, and either the project uses ref (reading a project
// grants read on what it uses) or the actor can read the asset directly.
//
// A used asset keeps resolving while it's in the trash, until the purge
// removes it (and its uses): then the ref is ErrAssetNotFound. Direct
// access, like DownloadURL, only serves live assets.
func (m *Manager) ResolveRef(ctx context.Context, actorID, projectID string, ref dam.AssetRef) (ResolvedRef, error) {
	if err := ref.Validate(); err != nil {
		return ResolvedRef{}, fmt.Errorf("%w: %w", dam.ErrInvalidQuery, err)
	}
	if projectID == "" {
		return ResolvedRef{}, fmt.Errorf("%w: empty project ID", dam.ErrInvalidQuery)
	}
	// The project first: someone who can't read it learns nothing about
	// what it uses, or whether the asset exists.
	if err := m.canRead(ctx, actorID, ledger.Owner{Kind: ledger.OwnerProject, ID: projectID}, ""); err != nil {
		return ResolvedRef{}, err
	}
	a, err := m.store.GetAsset(ctx, ref.AssetID)
	if err != nil {
		return ResolvedRef{}, err
	}
	used, err := m.store.HasUse(ctx, projectID, ref)
	if err != nil {
		return ResolvedRef{}, err
	}
	if !used {
		if err := m.canRead(ctx, actorID, a.Owner, a.ProjectID); err != nil {
			return ResolvedRef{}, err
		}
		if a.DeletedAt != nil {
			return ResolvedRef{}, dam.ErrAssetNotFound
		}
	}

	v, err := m.store.GetVersion(ctx, ref.AssetID, ref.Version)
	if err != nil {
		return ResolvedRef{}, err
	}
	// A trashed asset's ledger object may be marked deleted too (an owner
	// deletion marks both); its file stays until the purge, so that's fine.
	obj, err := m.ledger.Get(ctx, v.ObjectID)
	if errors.Is(err, ledger.ErrNotFound) {
		return ResolvedRef{}, fmt.Errorf("%w: %s's file is missing from the ledger", dam.ErrAssetNotFound, ref)
	}
	if err != nil {
		return ResolvedRef{}, fmt.Errorf("look up %s's file: %w", ref, err)
	}
	dl, err := m.presignDownload(ctx, obj, downloadFilename(a.Name, v.ContentType))
	if err != nil {
		return ResolvedRef{}, err
	}
	return ResolvedRef{Asset: a, Version: v, Download: dl}, nil
}
