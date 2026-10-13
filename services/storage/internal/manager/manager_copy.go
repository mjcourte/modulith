package manager

// Copies: cloning a project's files (CopyProjectFiles) and forking one asset
// version (ForkAsset). Both make independent copies: a new asset at version
// 1, with its own ledger object and its own S3 file, because the purge
// deletes a file together with its object, so files are never shared.
//
// Copies get deterministic IDs (dam.CopyAssetID), so every step can be
// repeated: a retried or resumed copy lands on the same rows and key.

import (
	"context"
	"errors"
	"fmt"
	"path"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

// copyVersion copies version v of src into a new asset owned by target, in
// folderID ("" for the top level), for project projectID (which dates the
// copy's ID). Steps, each safe to repeat, in an order that never leaves an
// S3 file the ledger doesn't know about:
//
//  1. record the ledger object (under its derived ID)
//  2. copy the file server-side (copying again overwrites it)
//  3. copy the media info
//  4. create the asset under its derived ID: the "done" marker, so a
//     repeat that finds it returns at once
func (m *Manager) copyVersion(ctx context.Context, actorID, projectID string, src dam.Asset, v dam.Version, target ledger.Owner, folderID string) (dam.Asset, error) {
	id := dam.CopyAssetID(projectID, target, dam.AssetRef{AssetID: src.ID, Version: v.Number})
	if a, err := m.store.GetAsset(ctx, id); err == nil {
		return a, nil
	} else if !errors.Is(err, dam.ErrAssetNotFound) {
		return dam.Asset{}, err
	}

	srcObj, err := m.ledger.Get(ctx, v.ObjectID)
	if errors.Is(err, ledger.ErrNotFound) {
		return dam.Asset{}, fmt.Errorf("%w: %s@%d's file is gone", dam.ErrAssetNotFound, src.ID, v.Number)
	}
	if err != nil {
		return dam.Asset{}, fmt.Errorf("look up %s@%d's file: %w", src.ID, v.Number, err)
	}
	assoc := "" // a project's copy belongs to that project; a library copy to none
	if target.Kind == ledger.OwnerProject {
		assoc = target.ID
	}
	objID := dam.CopyObjectID(projectID, id)
	key, err := keyFor(assoc, ledger.KindAsset, objID)
	if err != nil {
		return dam.Asset{}, err
	}

	if _, err := m.ledger.Create(ctx, ledger.Object{
		ID: objID, Owner: target, CreatedBy: actorID, ProjectID: assoc, Kind: ledger.KindAsset,
		Bucket: srcObj.Bucket, Key: key, SizeBytes: srcObj.SizeBytes, ContentType: srcObj.ContentType,
	}); err != nil {
		return dam.Asset{}, fmt.Errorf("record copy in ledger: %w", err)
	}
	if err := m.blobs.Copy(ctx, srcObj.Bucket, srcObj.Key, key); err != nil {
		return dam.Asset{}, fmt.Errorf("copy %s@%d's file: %w", src.ID, v.Number, err)
	}
	info, err := m.ledger.GetMediaInfo(ctx, srcObj.ID)
	switch {
	case err == nil:
		info.ObjectID = objID
		if err := m.ledger.SetMediaInfo(ctx, info); err != nil {
			return dam.Asset{}, fmt.Errorf("copy media info: %w", err)
		}
	case !errors.Is(err, ledger.ErrNotFound): // not found: nothing to copy
		return dam.Asset{}, fmt.Errorf("read media info: %w", err)
	}

	return m.store.CreateAsset(ctx, dam.Asset{
		ID: id, Owner: target, ProjectID: assoc, FolderID: folderID,
		Name: src.Name, Description: src.Description, Tags: src.Tags, CreatedBy: actorID,
	}, dam.Version{ObjectID: objID, ContentType: v.ContentType, SizeBytes: v.SizeBytes, CreatedBy: actorID})
}

// keyFor is objectKey with a chosen ID, for files whose key must come out
// the same on every attempt.
func keyFor(projectID string, kind ledger.Kind, id string) (string, error) {
	key, err := objectKey(projectID, kind)
	if err != nil {
		return "", err
	}
	return path.Join(path.Dir(key), id), nil
}

// CopyReport says what CopyProjectFiles copied (or found already copied).
type CopyReport struct {
	Folders int
	Assets  int // copies made for live assets the project owns, and for versions it uses
	Uses    int // uses recorded in the target, one per use of the source
}

// CopyProjectFiles copies a project's files into another project, for a
// clone: iam has already decided the actor may (CloneProject), so this is
// service to service only and checks nothing; the actor is recorded as the
// copies' creator. Into project to, owned by it, it copies:
//
//   - the folder tree
//   - each live asset project from owns, at its current version, into the
//     same folder
//   - every version project from uses, whoever owns it (trashed ones too:
//     the document still shows them), at the top level unless from owns it
//
// and records the target's uses of those copies, so the clone uses its own
// copies exactly where the source used originals. Each copied version is a
// new asset at version 1, independent of the source both ways. It's
// resumable: running it again after a failure finishes the job, and on a
// finished job changes nothing.
func (m *Manager) CopyProjectFiles(ctx context.Context, actorID, from, to string) (CopyReport, error) {
	switch {
	case from == "" || to == "":
		return CopyReport{}, fmt.Errorf("%w: both projects are needed", dam.ErrInvalidQuery)
	case from == to:
		return CopyReport{}, fmt.Errorf("%w: can't copy project %s into itself", dam.ErrInvalidQuery, from)
	}
	src := ledger.Owner{Kind: ledger.OwnerProject, ID: from}
	dst := ledger.Owner{Kind: ledger.OwnerProject, ID: to}
	var r CopyReport
	copies := map[string]bool{} // asset copies made or found, each counted once

	// Folders, parents before children, so each copy's parent exists.
	folders := map[string]string{"": ""} // source folder ID -> copy's
	for queue := []string{""}; len(queue) > 0; queue = queue[1:] {
		children, err := m.store.ListFolders(ctx, src, queue[0])
		if err != nil {
			return r, err
		}
		for _, f := range children {
			c, err := m.store.CreateFolder(ctx, dam.Folder{
				ID: dam.CopyFolderID(to, dst, f.ID), Owner: dst, ParentID: folders[queue[0]],
				Name: f.Name, CreatedBy: actorID,
			})
			if err != nil {
				return r, fmt.Errorf("copy folder %s: %w", f.ID, err)
			}
			folders[f.ID] = c.ID
			queue = append(queue, f.ID)
			r.Folders++
		}
	}

	// Live assets the project owns, at their current version.
	for token := ""; ; {
		page, err := m.store.ListAssets(ctx, dam.AssetQuery{Owner: src, PageSize: dam.MaxPageSize, PageToken: token})
		if err != nil {
			return r, err
		}
		for _, a := range page.Assets {
			v, err := m.store.GetVersion(ctx, a.ID, a.CurrentVersion)
			if err != nil {
				return r, err
			}
			c, err := m.copyVersion(ctx, actorID, to, a, v, dst, folders[a.FolderID])
			if err != nil {
				return r, err
			}
			copies[c.ID] = true
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}

	// Every version the project uses. A version that's also a current one
	// above maps to the same copy (same derived ID), so it isn't copied twice.
	uses, err := m.store.ListUses(ctx, from)
	if err != nil {
		return r, err
	}
	for _, ref := range uses {
		a, err := m.store.GetAsset(ctx, ref.AssetID)
		if err != nil {
			return r, err // uses go when their asset is purged, so it exists
		}
		v, err := m.store.GetVersion(ctx, ref.AssetID, ref.Version)
		if err != nil {
			return r, err
		}
		folder := ""
		if a.Owner == src {
			folder = folders[a.FolderID]
		}
		c, err := m.copyVersion(ctx, actorID, to, a, v, dst, folder)
		if err != nil {
			return r, err
		}
		copies[c.ID] = true
		if err := m.store.RecordUse(ctx, to, dam.AssetRef{AssetID: c.ID, Version: 1}); err != nil {
			return r, fmt.Errorf("record use of %s's copy: %w", ref, err)
		}
		r.Uses++
	}
	r.Assets = len(copies)
	return r, nil
}

// ForkAsset makes the version ref an independent copy, for editing an asset
// the actor may use but not change (gyoza's, another owner's). The copy is
// owned by target: the project itself (zero target, the default) or the
// project's owner (into their library), and projectID's use of ref moves to
// it. It returns the copy: its ref is {ID, 1}.
//
// The actor must be able to write the project, and read ref: through the
// project's use of it (a trashed but used asset can be forked, rescuing it
// before the purge), through the use of it by project via (one the actor
// reads, e.g. copying a file out of a shared project: a copy is no more than
// they could download), or directly. via is optional (""). Forking the same
// version again for the same target returns the same copy.
func (m *Manager) ForkAsset(ctx context.Context, actorID, projectID string, ref dam.AssetRef, target ledger.Owner, via string) (dam.Asset, error) {
	if err := ref.Validate(); err != nil {
		return dam.Asset{}, fmt.Errorf("%w: %w", dam.ErrInvalidQuery, err)
	}
	if err := m.canWriteProject(ctx, actorID, projectID); err != nil {
		return dam.Asset{}, err
	}
	project := ledger.Owner{Kind: ledger.OwnerProject, ID: projectID}
	if target.IsZero() {
		target = project
	}
	if target != project {
		owner, err := m.authz.ProjectOwner(ctx, projectID)
		if err != nil {
			return dam.Asset{}, err
		}
		if target != owner {
			return dam.Asset{}, fmt.Errorf("%w: a fork goes to the project or its owner, not %s %q", ErrForbidden, target.Kind, target.ID)
		}
		if err := m.canWrite(ctx, actorID, target, ""); err != nil {
			return dam.Asset{}, err
		}
	}

	a, err := m.store.GetAsset(ctx, ref.AssetID)
	if err != nil {
		return dam.Asset{}, err
	}
	used, err := m.store.HasUse(ctx, projectID, ref)
	if err != nil {
		return dam.Asset{}, err
	}
	if !used && via != "" {
		// Read through another project: one the actor can read, that uses ref.
		if err := m.canRead(ctx, actorID, ledger.Owner{Kind: ledger.OwnerProject, ID: via}, ""); err != nil {
			return dam.Asset{}, err
		}
		if used, err = m.store.HasUse(ctx, via, ref); err != nil {
			return dam.Asset{}, err
		}
	}
	if !used {
		if err := m.canRead(ctx, actorID, a.Owner, a.ProjectID); err != nil {
			return dam.Asset{}, err
		}
		if a.DeletedAt != nil {
			return dam.Asset{}, dam.ErrAssetNotFound
		}
	}
	v, err := m.store.GetVersion(ctx, ref.AssetID, ref.Version)
	if err != nil {
		return dam.Asset{}, err
	}

	c, err := m.copyVersion(ctx, actorID, projectID, a, v, target, "")
	if err != nil {
		return dam.Asset{}, err
	}
	if err := m.store.SwapUse(ctx, projectID, ref, dam.AssetRef{AssetID: c.ID, Version: 1}); err != nil {
		return dam.Asset{}, err
	}
	return c, nil
}
