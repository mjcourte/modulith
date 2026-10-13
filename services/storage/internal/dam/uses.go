package dam

import "context"

// UseStore records which asset versions each project uses, kept by
// documentmanager as refs are added to and removed from documents. Reading
// through a project and copying a project's files both start here.
// Project IDs are iam's; the Store doesn't check they exist.
type UseStore interface {
	// RecordUse records that projectID uses ref. Recording it again changes
	// nothing. A new use needs a live asset and an existing version:
	// otherwise ErrAssetNotFound / ErrVersionNotFound.
	RecordUse(ctx context.Context, projectID string, ref AssetRef) error
	// ForgetUse removes that use; forgetting one that isn't recorded is fine.
	ForgetUse(ctx context.Context, projectID string, ref AssetRef) error
	// SwapUse replaces projectID's use of from with a use of to, atomically
	// (a fork). to follows RecordUse's rules; from needn't be recorded, and
	// swapping again changes nothing.
	SwapUse(ctx context.Context, projectID string, from, to AssetRef) error
	// HasUse reports whether projectID uses ref.
	HasUse(ctx context.Context, projectID string, ref AssetRef) (bool, error)
	// ListUses returns every ref projectID uses, by asset ID then version.
	ListUses(ctx context.Context, projectID string) ([]AssetRef, error)
	// CountUses returns how many projects use each of assetIDs (any
	// version). Assets nobody uses are absent, so a lookup reads 0.
	CountUses(ctx context.Context, assetIDs []string) (map[string]int, error)
	// ForgetProjectUses removes all of projectID's uses, when the project is
	// deleted, and returns how many there were.
	ForgetProjectUses(ctx context.Context, projectID string) (int, error)
}
