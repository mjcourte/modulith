package dam

// Postgres: asset uses.

import (
	"context"
	"fmt"

	"gyoza/services/storage/internal/db"
)

func (p *Postgres) RecordUse(ctx context.Context, projectID string, ref AssetRef) error {
	if err := checkUse(projectID, ref); err != nil {
		return err
	}
	if !validID(ref.AssetID) {
		return ErrAssetNotFound
	}
	return p.inTx(ctx, func(q *db.Queries) error {
		return recordUseTx(ctx, q, projectID, ref)
	})
}

// recordUseTx is RecordUse inside a transaction.
func recordUseTx(ctx context.Context, q *db.Queries, projectID string, ref AssetRef) error {
	exists, err := q.UseExists(ctx, db.UseExistsParams{ProjectID: projectID, AssetID: ref.AssetID, Version: int32(ref.Version)})
	if err != nil {
		return fmt.Errorf("check use: %w", err)
	}
	if exists {
		return nil // already recorded, even if the asset has been trashed since
	}
	a, err := q.GetAssetForShare(ctx, ref.AssetID)
	if noRows(err) || (err == nil && a.DeletedAt != nil) {
		return ErrAssetNotFound
	}
	if err != nil {
		return fmt.Errorf("get asset: %w", err)
	}
	if _, err := q.GetVersion(ctx, db.GetVersionParams{AssetID: ref.AssetID, Number: int32(ref.Version)}); noRows(err) {
		return ErrVersionNotFound
	} else if err != nil {
		return fmt.Errorf("get version: %w", err)
	}
	if err := q.InsertUse(ctx, db.InsertUseParams{ProjectID: projectID, AssetID: ref.AssetID, Version: int32(ref.Version)}); err != nil {
		return fmt.Errorf("insert use: %w", err)
	}
	return nil
}

func (p *Postgres) SwapUse(ctx context.Context, projectID string, from, to AssetRef) error {
	if err := checkUse(projectID, from); err != nil {
		return err
	}
	if err := checkUse(projectID, to); err != nil {
		return err
	}
	if !validID(to.AssetID) {
		return ErrAssetNotFound
	}
	return p.inTx(ctx, func(q *db.Queries) error {
		if err := recordUseTx(ctx, q, projectID, to); err != nil {
			return err // rolled back: nothing changed
		}
		if from == to || !validID(from.AssetID) {
			return nil
		}
		if err := q.DeleteUse(ctx, db.DeleteUseParams{ProjectID: projectID, AssetID: from.AssetID, Version: int32(from.Version)}); err != nil {
			return fmt.Errorf("forget use: %w", err)
		}
		return nil
	})
}

func (p *Postgres) ForgetUse(ctx context.Context, projectID string, ref AssetRef) error {
	if err := checkUse(projectID, ref); err != nil {
		return err
	}
	if !validID(ref.AssetID) {
		return nil // never recorded
	}
	if err := p.q.DeleteUse(ctx, db.DeleteUseParams{ProjectID: projectID, AssetID: ref.AssetID, Version: int32(ref.Version)}); err != nil {
		return fmt.Errorf("forget use: %w", err)
	}
	return nil
}

func (p *Postgres) ListUses(ctx context.Context, projectID string) ([]AssetRef, error) {
	if err := checkProjectID(projectID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListUses(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list uses: %w", err)
	}
	out := make([]AssetRef, len(rows))
	for i, r := range rows {
		out[i] = AssetRef{AssetID: r.AssetID, Version: int(r.Version)}
	}
	return out, nil
}

func (p *Postgres) CountUses(ctx context.Context, assetIDs []string) (map[string]int, error) {
	ids := make([]string, 0, len(assetIDs))
	for _, id := range assetIDs {
		if validID(id) { // anything else can't be used, and would fail the uuid cast
			ids = append(ids, id)
		}
	}
	out := make(map[string]int)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := p.q.CountUses(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("count uses: %w", err)
	}
	for _, r := range rows {
		out[r.AssetID] = int(r.Projects)
	}
	return out, nil
}

func (p *Postgres) ForgetProjectUses(ctx context.Context, projectID string) (int, error) {
	if err := checkProjectID(projectID); err != nil {
		return 0, err
	}
	n, err := p.q.DeleteProjectUses(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("forget project uses: %w", err)
	}
	return int(n), nil
}

func (p *Postgres) HasUse(ctx context.Context, projectID string, ref AssetRef) (bool, error) {
	if err := checkUse(projectID, ref); err != nil {
		return false, err
	}
	if !validID(ref.AssetID) {
		return false, nil
	}
	ok, err := p.q.UseExists(ctx, db.UseExistsParams{ProjectID: projectID, AssetID: ref.AssetID, Version: int32(ref.Version)})
	if err != nil {
		return false, fmt.Errorf("check use: %w", err)
	}
	return ok, nil
}
