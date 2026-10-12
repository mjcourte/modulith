package manager_test

import (
	"testing"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

var (
	alice   = ledger.Owner{Kind: ledger.OwnerUser, ID: "alice"}
	acme    = ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}
	project = ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}
)

func ptr[T any](v T) *T { return &v } // for AssetUpdate fields

// mustCreateAsset creates a directly in the store with a first version of
// contentType, or fails the test.
func mustCreateAsset(t *testing.T, s dam.Store, a dam.Asset, contentType string) dam.Asset {
	t.Helper()
	if a.Name == "" {
		a.Name = "asset"
	}
	v := dam.Version{ObjectID: "obj-" + uuid.NewString(), ContentType: contentType, SizeBytes: 1024, CreatedBy: "alice"}
	created, err := s.CreateAsset(t.Context(), a, v)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	return created
}

func mustListVersions(t *testing.T, s dam.Store, assetID string) []dam.Version {
	t.Helper()
	vs, err := s.ListVersions(t.Context(), assetID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	return vs
}

// assetIDs lists the IDs of assets, for readable comparisons.
func assetIDs(assets []dam.Asset) []string {
	out := make([]string, len(assets))
	for i, a := range assets {
		out[i] = a.ID
	}
	return out
}

// folderNames lists the names of folders, for readable comparisons.
func folderNames(folders []dam.Folder) []string {
	out := make([]string, len(folders))
	for i, f := range folders {
		out[i] = f.Name
	}
	return out
}
