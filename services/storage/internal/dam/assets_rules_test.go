package dam

// Internal tests (package manager, not manager_test) because the
// helpers are unexported. Store behaviour is covered by the shared store suite.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

func TestCategoryOf(t *testing.T) {
	tests := map[string]Category{
		"image/png":                       CategoryImage,
		"image/svg+xml":                   CategoryVector,
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
		id := NewID()
		u, err := uuid.Parse(id)
		if err != nil || u.Version() != 7 {
			t.Fatalf("NewID() = %q: not a UUIDv7 (%v)", id, err)
		}
		// Paging relies on string order matching creation order.
		if id <= prev {
			t.Fatalf("NewID() = %q, not after previous %q", id, prev)
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
	assets := []Asset{{ID: NewID()}, {ID: NewID()}, {ID: NewID()}}
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

func TestAssetRefText(t *testing.T) {
	valid := map[string]AssetRef{
		"a1@1":         {AssetID: "a1", Version: 1},
		"a1@42":        {AssetID: "a1", Version: 42},
		"odd@name@3":   {AssetID: "odd@name", Version: 3}, // splits at the last @
		"0198-uuid@10": {AssetID: "0198-uuid", Version: 10},
	}
	for text, want := range valid {
		var got AssetRef
		if err := got.UnmarshalText([]byte(text)); err != nil {
			t.Errorf("UnmarshalText(%q): %v", text, err)
			continue
		}
		if got != want {
			t.Errorf("UnmarshalText(%q) = %+v, want %+v", text, got, want)
		}
		out, err := got.MarshalText()
		if err != nil || string(out) != text || got.String() != text {
			t.Errorf("%+v: MarshalText = %q, %v; String = %q; want %q", got, out, err, got.String(), text)
		}
	}

	invalid := []string{"", "a1", "a1@", "@3", "a1@0", "a1@-1", "a1@03", "a1@+3", "a1@ 3", "a1@3x", "a1@1.5"}
	for _, text := range invalid {
		before := AssetRef{AssetID: "keep", Version: 7}
		r := before
		if err := r.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("UnmarshalText(%q) = %+v, want an error", text, r)
		} else if r != before {
			t.Errorf("UnmarshalText(%q) changed the ref to %+v on error", text, r)
		}
	}

	for _, r := range []AssetRef{{}, {AssetID: "a1"}, {Version: 1}, {AssetID: "a1", Version: -2}} {
		if _, err := r.MarshalText(); err == nil {
			t.Errorf("MarshalText(%+v): want an error", r)
		}
	}
}

func TestAssetRefJSON(t *testing.T) {
	type doc struct {
		Ref  AssetRef         `json:"ref"`
		Uses map[AssetRef]int `json:"uses"`
	}
	in := doc{
		Ref:  AssetRef{AssetID: "a1", Version: 2},
		Uses: map[AssetRef]int{{AssetID: "b2", Version: 5}: 3},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"ref":"a1@2","uses":{"b2@5":3}}`
	if string(b) != want {
		t.Fatalf("Marshal = %s, want %s", b, want)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ref != in.Ref || len(out.Uses) != 1 || out.Uses[AssetRef{AssetID: "b2", Version: 5}] != 3 {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
	if err := json.Unmarshal([]byte(`{"ref":"a1@0"}`), &out); err == nil {
		t.Error(`Unmarshal of "a1@0": want an error`)
	}
}

// Copy IDs are the contract with documents (it computes a clone's
// refs): deterministic, distinct per input, valid UUIDv7s dated like the
// project they're made for.
func TestCopyAssetID(t *testing.T) {
	project := uuid.Must(uuid.NewV7()).String()
	target := ledger.Owner{Kind: ledger.OwnerProject, ID: project}
	src := AssetRef{AssetID: NewID(), Version: 2}
	id := CopyAssetID(project, target, src)

	if again := CopyAssetID(project, target, src); again != id {
		t.Errorf("CopyAssetID isn't deterministic: %s then %s", id, again)
	}
	u, err := uuid.Parse(id)
	if err != nil || u.Version() != 7 || u.Variant() != uuid.RFC4122 {
		t.Fatalf("CopyAssetID = %q (%v): want a UUIDv7", id, err)
	}
	p := uuid.MustParse(project)
	if u.Time() != p.Time() {
		t.Errorf("copy dated %v, want the project's %v", u.Time(), p.Time())
	}

	others := map[string]string{
		"another version":    CopyAssetID(project, target, AssetRef{AssetID: src.AssetID, Version: 3}),
		"another asset":      CopyAssetID(project, target, AssetRef{AssetID: NewID(), Version: 2}),
		"another target":     CopyAssetID(project, ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}, src),
		"another project":    CopyAssetID(uuid.Must(uuid.NewV7()).String(), target, src),
		"folder, same parts": CopyFolderID(project, target, src.AssetID),
		"its ledger object":  CopyObjectID(project, id),
	}
	for name, other := range others {
		if other == id {
			t.Errorf("%s gives the same ID %s", name, id)
		}
	}
	// Project IDs that aren't UUIDv7s (tests, tooling) still give valid IDs.
	if u, err := uuid.Parse(CopyAssetID("p1", target, src)); err != nil || u.Version() != 7 {
		t.Errorf("CopyAssetID for project p1 = %v, %v; want a UUIDv7", u, err)
	}
}
