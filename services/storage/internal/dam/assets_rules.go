package dam

// Validation, normalisation and paging helpers shared by every Store
// implementation, so Memory and Postgres accept and reject exactly the same
// input (the same role as normalize in ../ledger/ledger.go).

import (
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// NewID returns a new UUIDv7 as a string. UUIDv7s start with a timestamp, so
// IDs sort in creation order: paging can order by ID alone, and Postgres
// inserts land at the end of the index instead of at random pages.
func NewID() string {
	// NewV7 only fails if the system's random source does, which Go treats as
	// fatal anyway; uuid.Must panics in that case instead of making every
	// caller handle an error that can't really happen.
	return uuid.Must(uuid.NewV7()).String()
}

// normalizeTag trims and lowercases one tag, so "Logo", " logo" and "LOGO"
// are the same tag.
func normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

// normalizeTags normalises every tag, drops empty ones, removes duplicates and
// sorts the result, enforcing MaxTagLen and MaxTags.
func normalizeTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = normalizeTag(t)
		if t == "" {
			continue
		}
		if n := utf8.RuneCountInString(t); n > MaxTagLen {
			return nil, fmt.Errorf("%w: tag %q is %d characters, max %d", ErrInvalidAsset, t, n, MaxTagLen)
		}
		out = append(out, t)
	}
	slices.Sort(out)
	out = slices.Compact(out) // removes adjacent duplicates, which sorting made adjacent
	if len(out) > MaxTags {
		return nil, fmt.Errorf("%w: %d tags, max %d", ErrInvalidAsset, len(out), MaxTags)
	}
	return out, nil
}

// normalizeName trims a name and checks it is non-empty and within MaxNameLen.
// invalid is the error to wrap (ErrInvalidAsset or ErrInvalidFolder), so asset
// and folder names share the rule but report their own error.
func normalizeName(name string, invalid error) (string, error) {
	name = strings.TrimSpace(name)
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return "", fmt.Errorf("%w: name is empty", invalid)
	case n > MaxNameLen:
		return "", fmt.Errorf("%w: name is %d characters, max %d", invalid, n, MaxNameLen)
	}
	return name, nil
}

// checkOptionalID accepts "" (none) or a well-formed ID. Whether the ID exists
// is the Store's job; this only rejects garbage before it reaches a query.
func checkOptionalID(id, what string, invalid error) error {
	if id == "" {
		return nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("%w: %s %q is not a valid ID", invalid, what, id)
	}
	return nil
}

// checkDescription enforces MaxDescriptionLen. Descriptions are kept as typed.
func checkDescription(desc string) error {
	if n := utf8.RuneCountInString(desc); n > MaxDescriptionLen {
		return fmt.Errorf("%w: description is %d characters, max %d", ErrInvalidAsset, n, MaxDescriptionLen)
	}
	return nil
}

// validate checks the fields a caller provides for a version. Number,
// AssetID and CreatedAt are assigned by the Store, so they aren't checked.
func (v Version) validate() error {
	switch {
	case v.ObjectID == "":
		return fmt.Errorf("%w: version has no ledger object ID", ErrInvalidAsset)
	case v.ContentType == "":
		return fmt.Errorf("%w: version has no content type", ErrInvalidAsset)
	case v.SizeBytes < 0:
		return fmt.Errorf("%w: negative size %d", ErrInvalidAsset, v.SizeBytes)
	}
	return nil
}

// normalize validates a new asset and its first version for CreateAsset, and
// derives the fields that come from them: trimmed name, normalised tags,
// ProjectID for project-owned assets, and ContentType, Category and
// CurrentVersion from the first version. IDs and timestamps are left to the Store.
func (a Asset) normalize(first Version) (Asset, error) {
	if err := checkOptionalID(a.ID, "asset", ErrInvalidAsset); err != nil {
		return a, err
	}
	a, err := a.normalizeFields()
	if err != nil {
		return a, err
	}
	if err := first.validate(); err != nil {
		return a, err
	}
	a.ContentType = first.ContentType
	a.Category = CategoryOf(first.ContentType)
	a.CurrentVersion = 1
	return a, nil
}

// normalizeFields validates and normalises the fields a caller sets on a new
// asset (owner, project, name, folder, description, tags), without its first
// version. Upload sessions use it to check a new asset before its file exists.
func (a Asset) normalizeFields() (Asset, error) {
	if err := a.Owner.Validate(); err != nil {
		return a, fmt.Errorf("%w: %w", ErrInvalidAsset, err)
	}
	// Same rule as ledger objects: a project-owned asset belongs to its own project.
	if a.Owner.Kind == ledger.OwnerProject {
		switch a.ProjectID {
		case "":
			a.ProjectID = a.Owner.ID
		case a.Owner.ID:
		default:
			return a, fmt.Errorf("%w: owned by project %s but ProjectID is %s", ErrInvalidAsset, a.Owner.ID, a.ProjectID)
		}
	}
	var err error
	if a.Name, err = normalizeName(a.Name, ErrInvalidAsset); err != nil {
		return a, err
	}
	if err := checkOptionalID(a.FolderID, "folder", ErrInvalidAsset); err != nil {
		return a, err
	}
	if err := checkDescription(a.Description); err != nil {
		return a, err
	}
	if a.Tags, err = normalizeTags(a.Tags); err != nil {
		return a, err
	}
	return a, nil
}

// acceptsVersion reports whether v may become a new version of a: it must be
// valid and keep the asset's Category (png -> jpeg yes, image -> video no).
func (a Asset) acceptsVersion(v Version) error {
	if err := v.validate(); err != nil {
		return err
	}
	if c := CategoryOf(v.ContentType); c != a.Category {
		return fmt.Errorf("%w: %s version (%s) for a %s asset", ErrInvalidAsset, c, v.ContentType, a.Category)
	}
	return nil
}

// normalize validates a partial update, normalising the fields it sets.
func (u AssetUpdate) normalize() (AssetUpdate, error) {
	if u.Name != nil {
		name, err := normalizeName(*u.Name, ErrInvalidAsset)
		if err != nil {
			return u, err
		}
		u.Name = &name
	}
	if u.FolderID != nil {
		if err := checkOptionalID(*u.FolderID, "folder", ErrInvalidAsset); err != nil {
			return u, err
		}
	}
	if u.Description != nil {
		if err := checkDescription(*u.Description); err != nil {
			return u, err
		}
	}
	if u.Tags != nil {
		tags, err := normalizeTags(*u.Tags)
		if err != nil {
			return u, err
		}
		u.Tags = &tags
	}
	return u, nil
}

// knownCategories is every valid Category, for checking query filters.
var knownCategories = []Category{
	CategoryImage, CategoryVector, CategoryVideo, CategoryAudio, CategoryFont,
	CategoryPDF, CategoryJSON, CategoryText, CategoryOther,
}

// normalize validates q and fills in defaults: the page size, and the tag
// filter normalised the same way stored tags are.
func (q AssetQuery) normalize() (AssetQuery, error) {
	hasProject, hasOwner := q.ProjectID != "", !q.Owner.IsZero()
	if hasProject == hasOwner {
		return q, fmt.Errorf("%w: set exactly one of ProjectID or Owner", ErrInvalidQuery)
	}
	if hasOwner {
		if err := q.Owner.Validate(); err != nil {
			return q, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
	}
	if q.Category != "" && !slices.Contains(knownCategories, q.Category) {
		return q, fmt.Errorf("%w: unknown category %q", ErrInvalidQuery, q.Category)
	}
	q.Tag = normalizeTag(q.Tag)
	if q.OnlyDeleted {
		q.IncludeDeleted = true
	}
	if q.Folder != nil {
		if err := checkOptionalID(*q.Folder, "folder", ErrInvalidQuery); err != nil {
			return q, err
		}
	}
	switch {
	case q.PageSize < 0:
		return q, fmt.Errorf("%w: negative PageSize", ErrInvalidQuery)
	case q.PageSize == 0:
		q.PageSize = DefaultPageSize
	case q.PageSize > MaxPageSize:
		q.PageSize = MaxPageSize
	}
	return q, nil
}

// Paging: assets are ordered by ID. UUIDv7 strings sort in creation order, so
// a page token only needs the last ID seen. It is base64-encoded so callers
// treat it as opaque and don't build tokens themselves.

// afterID returns the ID the requested page starts after, or "" for the first page.
func (q AssetQuery) afterID() (string, error) {
	if q.PageToken == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.PageToken)
	if err != nil {
		return "", fmt.Errorf("%w: bad page token", ErrInvalidQuery)
	}
	if _, err := uuid.Parse(string(raw)); err != nil {
		return "", fmt.Errorf("%w: bad page token", ErrInvalidQuery)
	}
	return string(raw), nil
}

// assetPageOf turns up to pageSize+1 matches, sorted by ID, into a page; the
// extra match only signals that another page exists.
func assetPageOf(matches []Asset, pageSize int) AssetPage {
	if len(matches) <= pageSize {
		return AssetPage{Assets: matches}
	}
	matches = matches[:pageSize]
	last := matches[pageSize-1].ID
	return AssetPage{Assets: matches, NextPageToken: base64.RawURLEncoding.EncodeToString([]byte(last))}
}

var (
	_ encoding.TextMarshaler   = AssetRef{}
	_ encoding.TextUnmarshaler = (*AssetRef)(nil)
)

func (r AssetRef) Validate() error {
	if r.AssetID == "" {
		return errors.New("AssetRef needs an AssetID")
	}
	if r.Version < 1 {
		return fmt.Errorf("AssetRef.Version must be >=1, got: %d", r.Version)
	}
	return nil
}

// String formats r as "<asset ID>@<version>", the same text MarshalText writes.
func (r AssetRef) String() string {
	return r.AssetID + "@" + strconv.Itoa(r.Version)
}

// MarshalText makes JSON (values and map keys) encode r as "<asset ID>@<version>".
// Invalid refs are refused, so one is never written out.
func (r AssetRef) MarshalText() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return []byte(r.String()), nil
}

// UnmarshalText parses "<asset ID>@<version>". Each ref has exactly one text
// form: versions like "03" or "+3" are refused. On error r is left unchanged.
func (r *AssetRef) UnmarshalText(b []byte) error {
	s := string(b)
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return fmt.Errorf("AssetRef %q: want <asset ID>@<version>", s)
	}
	digits := s[at+1:]
	v, err := strconv.Atoi(digits)
	if err != nil || strconv.Itoa(v) != digits {
		return fmt.Errorf("AssetRef %q: bad version %q", s, digits)
	}
	ref := AssetRef{AssetID: s[:at], Version: v}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("AssetRef %q: %w", s, err)
	}
	*r = ref
	return nil
}

// CopyAssetID is the ID of the copy of src made into target for project
// projectID (a clone, or the project a fork happens in): asset version 1 of
// that ID. The same inputs always give the same ID, so a retried copy lands
// on the same asset, and documents can work out a clone's rewritten
// refs ({CopyAssetID(clone, cloneOwner, ref), 1}) without a mapping table.
func CopyAssetID(projectID string, target ledger.Owner, src AssetRef) string {
	return derivedID(projectID, "asset", string(target.Kind), target.ID, src.AssetID, strconv.Itoa(src.Version))
}

// CopyFolderID is the ID of the copy of folder srcID made into target.
func CopyFolderID(projectID string, target ledger.Owner, srcID string) string {
	return derivedID(projectID, "folder", string(target.Kind), target.ID, srcID)
}

// CopyObjectID is the ID of the ledger object holding a copy's file.
func CopyObjectID(projectID, copyAssetID string) string {
	return derivedID(projectID, "object", copyAssetID)
}

// derivedID returns a deterministic ID shaped like a UUIDv7, so derived rows
// sort near the others of their time, as NewID's do: its 48-bit timestamp is
// timeFrom's, when that's a UUIDv7 (project IDs are), and the rest is a hash
// of parts (all of them, timeFrom included). Otherwise the timestamp bits
// come from the hash too: still deterministic, just not time-ordered.
func derivedID(timeFrom string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte("modulith.storage.derived-id"))
	for _, p := range append([]string{timeFrom}, parts...) {
		h.Write([]byte{0}) // separator, so ("ab", "c") and ("a", "bc") differ
		h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	var id uuid.UUID
	copy(id[:], sum)
	if t, err := uuid.Parse(timeFrom); err == nil && t.Version() == 7 {
		copy(id[:6], t[:6]) // the Unix-millisecond timestamp
	}
	id[6] = 0x70 | id[6]&0x0f // version 7
	id[8] = 0x80 | id[8]&0x3f // RFC 4122 variant
	return id.String()
}
