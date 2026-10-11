package storagemanager

// Internal tests of the upload session rules. Store behaviour is covered by
// the shared upload suite.

import (
	"errors"
	"testing"
	"time"

	"gyoza/services/storageledger"
)

func validUpload() Upload {
	return Upload{
		Owner:  storageledger.Owner{Kind: storageledger.OwnerOrg, ID: "acme"},
		Name:   " Logo ",
		Tags:   []string{"Brand", "brand"},
		Bucket: "gyoza", Key: "library/asset/x", ContentType: "image/png", SizeBytes: 10,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestUploadNormalize(t *testing.T) {
	u, err := validUpload().normalize()
	if err != nil || u.Name != "Logo" || len(u.Tags) != 1 || u.Tags[0] != "brand" {
		t.Errorf("new-asset session = %+v, %v; want name trimmed and tags normalised", u, err)
	}

	version := validUpload()
	version.Name, version.Tags, version.AssetID = "", nil, newID()
	if _, err := version.normalize(); err != nil {
		t.Errorf("new-version session: %v", err)
	}

	invalid := map[string]func(*Upload){
		"no owner":            func(u *Upload) { u.Owner = storageledger.Owner{} },
		"no key":              func(u *Upload) { u.Key = "" },
		"no content type":     func(u *Upload) { u.ContentType = "" },
		"negative size":       func(u *Upload) { u.SizeBytes = -1 },
		"no expiry":           func(u *Upload) { u.ExpiresAt = time.Time{} },
		"blank name":          func(u *Upload) { u.Name = " " },
		"garbage folder":      func(u *Upload) { u.FolderID = "garbage" },
		"version with a name": func(u *Upload) { u.AssetID = newID() },
		"garbage asset ID":    func(u *Upload) { u.Name, u.Tags, u.AssetID = "", nil, "garbage" },
	}
	for name, mutate := range invalid {
		u := validUpload()
		mutate(&u)
		if _, err := u.normalize(); !errors.Is(err, ErrInvalidUpload) {
			t.Errorf("%s: got %v, want ErrInvalidUpload", name, err)
		}
	}
	// Asset-rule failures match both errors.
	blank := validUpload()
	blank.Name = ""
	if _, err := blank.normalize(); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("blank name: got %v, want it to match ErrInvalidAsset too", err)
	}
}

func TestUploadMatchesAndExpiry(t *testing.T) {
	u := validUpload()
	if err := u.matches(Version{ContentType: "image/png", SizeBytes: 10}); err != nil {
		t.Errorf("matching file: %v", err)
	}
	for _, v := range []Version{{ContentType: "image/jpeg", SizeBytes: 10}, {ContentType: "image/png", SizeBytes: 11}} {
		if err := u.matches(v); !errors.Is(err, ErrInvalidUpload) {
			t.Errorf("matches(%+v) = %v, want ErrInvalidUpload", v, err)
		}
	}
	if u.expired(u.ExpiresAt.Add(-time.Second)) || !u.expired(u.ExpiresAt) {
		t.Error("a session should expire exactly at ExpiresAt")
	}
}
