package manager

// Internal tests (package manager, not manager_test) because the
// helpers are unexported. Store behaviour is covered by the shared store suite.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/ledger"
)

func TestCategoryOf(t *testing.T) {
	tests := map[string]Category{
		"image/png":                       CategoryImage,
		"image/svg+xml":                   CategoryImage,
		"IMAGE/JPEG":                      CategoryImage,
		"video/mp4":                       CategoryVideo,
		"audio/mpeg":                      CategoryAudio,
		"font/woff2":                      CategoryFont,
		"application/vnd.ms-fontobject":   CategoryFont,
		"application/x-font-ttf":          CategoryFont,
		"application/pdf":                 CategoryPDF,
		"application/json; charset=utf-8": CategoryJSON,
		"application/ld+json":             CategoryJSON,
		"text/plain":                      CategoryText,
		"application/zip":                 CategoryOther,
		"":                                CategoryOther,
		"not a mime type":                 CategoryOther,
	}
	for contentType, want := range tests {
		if got := CategoryOf(contentType); got != want {
			t.Errorf("CategoryOf(%q) = %q, want %q", contentType, got, want)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := normalizeTags([]string{" Logo", "LOGO", "brand", "", "  "})
	if err != nil {
		t.Fatalf("normalizeTags: %v", err)
	}
	if want := []string{"brand", "logo"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	if _, err := normalizeTags([]string{strings.Repeat("x", MaxTagLen+1)}); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("too-long tag: got %v, want ErrInvalidAsset", err)
	}
	tooMany := make([]string, MaxTags+1)
	for i := range tooMany {
		tooMany[i] = uuid.NewString() // distinct, so deduplication can't save it
	}
	if _, err := normalizeTags(tooMany); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("too many tags: got %v, want ErrInvalidAsset", err)
	}
}

func TestNewIDIsTimeOrderedUUIDv7(t *testing.T) {
	prev := ""
	for range 1000 {
		id := newID()
		u, err := uuid.Parse(id)
		if err != nil || u.Version() != 7 {
			t.Fatalf("newID() = %q: not a UUIDv7 (%v)", id, err)
		}
		// Paging relies on string order matching creation order.
		if id <= prev {
			t.Fatalf("newID() = %q, not after previous %q", id, prev)
		}
		prev = id
	}
}

var validVersion = Version{ObjectID: "obj1", ContentType: "image/png", SizeBytes: 10}

func TestAssetNormalize(t *testing.T) {
	user := ledger.Owner{Kind: ledger.OwnerUser, ID: "u1"}
	project := ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}

	a, err := Asset{Owner: project, Name: "  Logo  ", Tags: []string{"Brand"}}.normalize(validVersion)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if a.Name != "Logo" || a.ProjectID != "p1" || a.Category != CategoryImage ||
		a.ContentType != "image/png" || a.CurrentVersion != 1 || !slices.Equal(a.Tags, []string{"brand"}) {
		t.Errorf("normalize = %+v", a)
	}

	invalid := []struct {
		name  string
		asset Asset
		first Version
	}{
		{"no owner", Asset{Name: "x"}, validVersion},
		{"blank name", Asset{Owner: user, Name: "   "}, validVersion},
		{"long name", Asset{Owner: user, Name: strings.Repeat("x", MaxNameLen+1)}, validVersion},
		{"long description", Asset{Owner: user, Name: "x", Description: strings.Repeat("x", MaxDescriptionLen+1)}, validVersion},
		{"project owner, other project", Asset{Owner: project, ProjectID: "p2", Name: "x"}, validVersion},
		{"version without object", Asset{Owner: user, Name: "x"}, Version{ContentType: "image/png"}},
		{"version without type", Asset{Owner: user, Name: "x"}, Version{ObjectID: "o"}},
	}
	for _, tt := range invalid {
		if _, err := tt.asset.normalize(tt.first); !errors.Is(err, ErrInvalidAsset) {
			t.Errorf("%s: got %v, want ErrInvalidAsset", tt.name, err)
		}
	}
}

func TestAcceptsVersion(t *testing.T) {
	image := Asset{Category: CategoryImage}
	if err := image.acceptsVersion(Version{ObjectID: "o", ContentType: "image/jpeg"}); err != nil {
		t.Errorf("png asset, jpeg version: %v", err)
	}
	if err := image.acceptsVersion(Version{ObjectID: "o", ContentType: "video/mp4"}); !errors.Is(err, ErrInvalidAsset) {
		t.Errorf("image asset, video version: got %v, want ErrInvalidAsset", err)
	}
}

func TestAssetQueryNormalize(t *testing.T) {
	q, err := AssetQuery{ProjectID: "p1", Tag: " LOGO "}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if q.Tag != "logo" || q.PageSize != DefaultPageSize {
		t.Errorf("normalize = %+v", q)
	}
	if q, _ := (AssetQuery{ProjectID: "p1", PageSize: MaxPageSize + 1}).normalize(); q.PageSize != MaxPageSize {
		t.Errorf("PageSize = %d, want capped at %d", q.PageSize, MaxPageSize)
	}

	invalid := map[string]AssetQuery{
		"no scope":         {},
		"two scopes":       {ProjectID: "p1", Owner: ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}},
		"bad owner":        {Owner: ledger.Owner{Kind: ledger.OwnerUser}},
		"unknown category": {ProjectID: "p1", Category: "spreadsheet"},
		"negative size":    {ProjectID: "p1", PageSize: -1},
	}
	for name, q := range invalid {
		if _, err := q.normalize(); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%s: got %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestPageTokenRoundTrip(t *testing.T) {
	assets := []Asset{{ID: newID()}, {ID: newID()}, {ID: newID()}}
	page := assetPageOf(assets, 2) // 3 matches for a page of 2: another page exists
	if len(page.Assets) != 2 || page.NextPageToken == "" {
		t.Fatalf("page = %d assets, token %q", len(page.Assets), page.NextPageToken)
	}
	after, err := AssetQuery{PageToken: page.NextPageToken}.afterID()
	if err != nil || after != assets[1].ID {
		t.Errorf("afterID = %q, %v; want %q", after, err, assets[1].ID)
	}
	if last := assetPageOf(assets[:2], 2); last.NextPageToken != "" {
		t.Errorf("exact fit has token %q, want none", last.NextPageToken)
	}
	for _, bad := range []string{"!!", "bm90LWEtdXVpZA"} { // not base64; base64 of "not-a-uuid"
		if _, err := (AssetQuery{PageToken: bad}).afterID(); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("afterID(%q) = %v, want ErrInvalidQuery", bad, err)
		}
	}
}
