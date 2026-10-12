package dam

// Internal tests of the unexported folder rules. Store behaviour is covered by
// the shared folder suite (step 3c).

import (
	"errors"
	"strings"
	"testing"

	"gyoza/services/storage/internal/ledger"
)

func TestNormalizeFolderName(t *testing.T) {
	valid := map[string]string{
		"Logos":          "Logos", // case is kept
		"  2026 brand  ": "2026 brand",
		"Ärger & Co":     "Ärger & Co",
		"v1.2":           "v1.2",
		"console":        "console", // only the exact device names are reserved
		"emoji 🎨 folder": "emoji 🎨 folder",
	}
	for in, want := range valid {
		got, err := normalizeFolderName(in)
		if err != nil || got != want {
			t.Errorf("normalizeFolderName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	invalid := []string{
		"", "   ",
		"a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b",
		"tab\there", "nul\x00",
		"trailing.", ".", "..",
		"CON", "con", "Nul.txt", "LPT1", "com9.tar.gz",
		strings.Repeat("x", MaxNameLen+1),
	}
	for _, in := range invalid {
		if _, err := normalizeFolderName(in); !errors.Is(err, ErrInvalidFolder) {
			t.Errorf("normalizeFolderName(%q) = %v, want ErrInvalidFolder", in, err)
		}
	}
}

func TestFolderNameKey(t *testing.T) {
	for _, pair := range [][2]string{{"Logos", "logos"}, {"ÄRGER", "ärger"}, {"Brand Assets", "brand assets"}} {
		if folderNameKey(pair[0]) != folderNameKey(pair[1]) {
			t.Errorf("%q and %q should clash", pair[0], pair[1])
		}
	}
	if folderNameKey("Straße") == folderNameKey("Strasse") {
		t.Error(`"Straße" and "Strasse" should not clash (filesystems treat them as different)`)
	}
}

func TestFolderNormalize(t *testing.T) {
	org := ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}
	f, err := Folder{Owner: org, Name: " Logos ", ParentID: NewID()}.normalize()
	if err != nil || f.Name != "Logos" {
		t.Errorf("normalize = %+v, %v", f, err)
	}

	invalid := map[string]Folder{
		"no owner":     {Name: "x"},
		"bad name":     {Owner: org, Name: "a/b"},
		"bad parentID": {Owner: org, Name: "x", ParentID: "not-a-uuid"},
	}
	for name, f := range invalid {
		if _, err := f.normalize(); !errors.Is(err, ErrInvalidFolder) {
			t.Errorf("%s: got %v, want ErrInvalidFolder", name, err)
		}
	}
}

func TestCheckFolderDepth(t *testing.T) {
	if err := checkFolderDepth(MaxFolderDepth); err != nil {
		t.Errorf("depth %d: %v", MaxFolderDepth, err)
	}
	if err := checkFolderDepth(MaxFolderDepth + 1); !errors.Is(err, ErrInvalidFolder) {
		t.Errorf("depth %d: got %v, want ErrInvalidFolder", MaxFolderDepth+1, err)
	}
}

// Assets and queries only check that folder IDs are well-formed; existence is
// the Store's job.
func TestAssetFolderIDFormat(t *testing.T) {
	user := ledger.Owner{Kind: ledger.OwnerUser, ID: "u1"}
	v := Version{ObjectID: "o", ContentType: "image/png"}

	if _, err := (Asset{Owner: user, Name: "x", FolderID: NewID()}).normalize(v); err != nil {
		t.Errorf("asset in a folder: %v", err)
	}
	if _, err := (Asset{Owner: user, Name: "x", FolderID: "garbage"}).normalize(v); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("asset with garbage FolderID: got %v, want ErrInvalidAsset", err)
	}

	top, garbage := "", "garbage"
	if _, err := (AssetUpdate{FolderID: &top}).normalize(); err != nil {
		t.Errorf("move to top level: %v", err)
	}
	if _, err := (AssetUpdate{FolderID: &garbage}).normalize(); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("move to garbage folder: got %v, want ErrInvalidAsset", err)
	}
	if _, err := (AssetQuery{Owner: user, Folder: &top}).normalize(); err != nil {
		t.Errorf("query top level: %v", err)
	}
	if _, err := (AssetQuery{Owner: user, Folder: &garbage}).normalize(); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("query garbage folder: got %v, want ErrInvalidQuery", err)
	}
}
