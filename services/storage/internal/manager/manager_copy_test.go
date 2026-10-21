package manager_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	sm "github.com/mjcourte/modulith/services/storage/internal/manager"
)

var clone = ledger.Owner{Kind: ledger.OwnerProject, ID: "p2"}

// failingCopies fails server-side copies while fail is set.
type failingCopies struct {
	blob.Store
	fail bool
}

func (f *failingCopies) Copy(ctx context.Context, bucket, src, dst string) error {
	if f.fail {
		return errors.New("S3 unavailable")
	}
	return f.Store.Copy(ctx, bucket, src, dst)
}

// cloneSource fills project p1 (acme's): folders Brand/Logos; logo in Logos
// at version 2, its version 1 also used by the document; a trashed project
// file; and acme's library asset lib, used at version 1.
type cloneSource struct {
	brand, logos  dam.Folder
	logo, lib     dam.Asset
	v1, v2, libV1 []byte
}

func newCloneSource(t *testing.T, tm testManager) cloneSource {
	t.Helper()
	ctx := t.Context()
	tm.authz.SetProjectOwner("p1", acme)
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	var s cloneSource
	var err error
	if s.brand, err = tm.CreateFolder(ctx, "member", dam.Folder{Owner: project, Name: "Brand"}); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if s.logos, err = tm.CreateFolder(ctx, "member", dam.Folder{Owner: project, Name: "Logos", ParentID: s.brand.ID}); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	req := newAssetRequest()
	req.Owner, req.FolderID = project, s.logos.ID
	s.logo, s.v1 = uploadAsset(t, tm, "member", req)
	next := sm.UploadRequest{AssetID: s.logo.ID, ContentType: "image/png", SizeBytes: 4096}
	if _, err := tm.CompleteUpload(ctx, "member", beginAndPut(t, tm, "member", next).UploadID); err != nil {
		t.Fatalf("second version: %v", err)
	}
	s.v2 = fileOf("image/png", 4096)
	trash := newAssetRequest()
	trash.Owner = project
	trashed, _ := uploadAsset(t, tm, "member", trash)
	if err := tm.DeleteAsset(ctx, "member", trashed.ID); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	s.lib, s.libV1 = uploadAsset(t, tm, "member", newAssetRequest())
	for _, ref := range []dam.AssetRef{{AssetID: s.logo.ID, Version: 1}, {AssetID: s.lib.ID, Version: 1}} {
		if err := tm.RecordAssetUse(ctx, "member", "p1", ref); err != nil {
			t.Fatalf("RecordAssetUse(%s): %v", ref, err)
		}
	}
	return s
}

func copyOf(projectID string, target ledger.Owner, assetID string, version int) string {
	return dam.CopyAssetID(projectID, target, dam.AssetRef{AssetID: assetID, Version: version})
}

func cloneAssets(t *testing.T, tm testManager) []dam.Asset {
	t.Helper()
	page, err := tm.store.ListAssets(t.Context(), dam.AssetQuery{Owner: clone, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	return page.Assets
}

func TestCopyProjectFiles(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	s := newCloneSource(t, tm)
	tm.authz.AllowProject("cloner", "p2", sm.AccessRead)

	r, err := tm.CopyProjectFiles(ctx, "cloner", "p1", "p2")
	if err != nil {
		t.Fatalf("CopyProjectFiles: %v", err)
	}
	if want := (sm.CopyReport{Folders: 2, Assets: 3, Uses: 2}); r != want {
		t.Errorf("report = %+v, want %+v (logo@2, logo@1, lib@1; not the trashed file)", r, want)
	}

	// The folder tree, same shape.
	top, _ := tm.store.ListFolders(ctx, clone, "")
	if len(top) != 1 || top[0].Name != "Brand" {
		t.Fatalf("clone's top folders = %+v, want Brand", top)
	}
	sub, _ := tm.store.ListFolders(ctx, clone, top[0].ID)
	if len(sub) != 1 || sub[0].Name != "Logos" {
		t.Fatalf("clone's Brand holds %+v, want Logos", sub)
	}

	// Each copy: owned by the clone, version 1, made by the cloner, with the
	// source version's bytes, where the ID formula says.
	for _, c := range []struct {
		name   string
		src    dam.Asset
		ver    int
		data   []byte
		folder string
	}{
		{"logo, current", s.logo, 2, s.v2, sub[0].ID},
		{"logo, used older version", s.logo, 1, s.v1, sub[0].ID},
		{"acme's library asset", s.lib, 1, s.libV1, ""},
	} {
		id := copyOf("p2", clone, c.src.ID, c.ver)
		a, err := tm.store.GetAsset(ctx, id)
		if err != nil {
			t.Errorf("%s: no copy at %s: %v", c.name, id, err)
			continue
		}
		if a.Owner != clone || a.ProjectID != "p2" || a.CurrentVersion != 1 || a.CreatedBy != "cloner" || a.FolderID != c.folder || a.Name != c.src.Name {
			t.Errorf("%s: copy = %+v", c.name, a)
		}
		if data, _ := download(t, tm, "cloner", id, 1); !bytes.Equal(data, c.data) {
			t.Errorf("%s: copy's bytes aren't version %d's", c.name, c.ver)
		}
		v, _ := tm.store.GetVersion(ctx, id, 1)
		obj, err := tm.ledger.Get(ctx, v.ObjectID)
		if err != nil || obj.Owner != clone || obj.CreatedBy != "cloner" || !strings.HasPrefix(obj.Key, "projects/p2/") {
			t.Errorf("%s: copy's ledger object = %+v, %v", c.name, obj, err)
		}
		srcV, _ := tm.store.GetVersion(ctx, c.src.ID, c.ver)
		srcInfo, srcErr := tm.ledger.GetMediaInfo(ctx, srcV.ObjectID)
		info, err := tm.ledger.GetMediaInfo(ctx, v.ObjectID)
		if (srcErr == nil) != (err == nil) || info.Status != srcInfo.Status || info.Width != srcInfo.Width {
			t.Errorf("%s: media info %+v (%v), source's %+v (%v)", c.name, info, err, srcInfo, srcErr)
		}
	}
	wantUses := []dam.AssetRef{
		{AssetID: copyOf("p2", clone, s.logo.ID, 1), Version: 1},
		{AssetID: copyOf("p2", clone, s.lib.ID, 1), Version: 1},
	}
	got := mustUses(t, tm, "p2")
	slices.SortFunc(wantUses, func(a, b dam.AssetRef) int { return strings.Compare(a.AssetID, b.AssetID) })
	if !slices.Equal(got, wantUses) {
		t.Errorf("clone uses %v, want its copies %v", got, wantUses)
	}

	// Running it again finishes nothing new and duplicates nothing.
	if again, err := tm.CopyProjectFiles(ctx, "cloner", "p1", "p2"); err != nil || again != r {
		t.Errorf("CopyProjectFiles again = %+v, %v; want %+v", again, err, r)
	}
	if n := len(cloneAssets(t, tm)); n != 3 {
		t.Errorf("clone has %d assets after a second run, want 3", n)
	}
	if n := len(ledgerObjects(t, tm, clone, true)); n != 3 {
		t.Errorf("clone has %d ledger objects after a second run, want 3", n)
	}

	// Independent of the source: delete and purge it, the copies stay.
	if _, err := tm.DeleteOwned(ctx, project); err != nil {
		t.Fatalf("DeleteOwned: %v", err)
	}
	if _, err := tm.DeleteOwned(ctx, acme); err != nil {
		t.Fatalf("DeleteOwned: %v", err)
	}
	if _, err := tm.Cleanup(ctx, time.Now().Add(sm.DefaultTrashRetention+time.Hour)); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := tm.store.GetAsset(ctx, s.logo.ID); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Fatalf("source not purged: %v", err)
	}
	if data, _ := download(t, tm, "cloner", copyOf("p2", clone, s.lib.ID, 1), 1); !bytes.Equal(data, s.libV1) {
		t.Error("a copy lost its bytes when the source was purged")
	}
}

// A clone interrupted part-way resumes: the second run completes it without
// duplicating what the first did.
func TestCopyProjectFilesResumes(t *testing.T) {
	var flaky *failingCopies
	tm := newTestManagerWith(t, sm.Config{Bucket: testBucket}, func(m *blob.Memory) blob.Store {
		flaky = &failingCopies{Store: m}
		return flaky
	})
	ctx := t.Context()
	newCloneSource(t, tm)

	flaky.fail = true
	if _, err := tm.CopyProjectFiles(ctx, "cloner", "p1", "p2"); err == nil {
		t.Fatal("CopyProjectFiles with S3 failing: want an error")
	}
	if n := len(cloneAssets(t, tm)); n != 0 {
		t.Errorf("a failed copy left %d assets: an asset must only appear once its file is copied", n)
	}
	flaky.fail = false
	r, err := tm.CopyProjectFiles(ctx, "cloner", "p1", "p2")
	if err != nil || r.Assets != 3 || r.Uses != 2 {
		t.Fatalf("resumed CopyProjectFiles = %+v, %v", r, err)
	}
	if n := len(ledgerObjects(t, tm, clone, true)); n != 3 {
		t.Errorf("clone has %d ledger objects, want 3: one per copy, retries included", n)
	}
}

func TestCopyProjectFilesRejects(t *testing.T) {
	tm := newTestManager(t)
	for _, c := range [][2]string{{"p1", "p1"}, {"", "p2"}, {"p1", ""}} {
		if _, err := tm.CopyProjectFiles(t.Context(), "cloner", c[0], c[1]); !errors.Is(err, dam.ErrInvalidQuery) {
			t.Errorf("CopyProjectFiles(%q, %q) = %v, want ErrInvalidQuery", c[0], c[1], err)
		}
	}
}

// forkSetup: p1 (acme's, member edits it) uses modulith's stock image; p3 is
// guest's own project, and guest reads p1.
func forkSetup(t *testing.T) (testManager, dam.Asset, []byte) {
	t.Helper()
	tm := newTestManager(t)
	modulith := ledger.Owner{Kind: ledger.OwnerModulith}
	tm.authz.AllowOwner("tooling", modulith, sm.AccessWrite)
	tm.authz.AllowOwner("member", modulith, sm.AccessRead)
	tm.authz.SetProjectOwner("p1", acme)
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	tm.authz.SetProjectOwner("p3", ledger.Owner{Kind: ledger.OwnerUser, ID: "guest"})
	tm.authz.AllowProject("guest", "p3", sm.AccessWrite)
	tm.authz.AllowProject("guest", "p1", sm.AccessRead)
	req := newAssetRequest()
	req.Owner = modulith
	stock, data := uploadAsset(t, tm, "tooling", req)
	if err := tm.RecordAssetUse(t.Context(), "member", "p1", dam.AssetRef{AssetID: stock.ID, Version: 1}); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	return tm, stock, data
}

func TestForkAsset(t *testing.T) {
	tm, stock, data := forkSetup(t)
	ctx := t.Context()
	ref := dam.AssetRef{AssetID: stock.ID, Version: 1}

	fork, err := tm.ForkAsset(ctx, "member", "p1", ref, ledger.Owner{}, "")
	if err != nil {
		t.Fatalf("ForkAsset: %v", err)
	}
	if fork.ID != copyOf("p1", project, stock.ID, 1) || fork.Owner != project || fork.CurrentVersion != 1 || fork.CreatedBy != "member" {
		t.Errorf("fork = %+v; want p1's copy, version 1, made by member", fork)
	}
	if got, _ := download(t, tm, "member", fork.ID, 1); !bytes.Equal(got, data) {
		t.Error("the fork's bytes aren't the original's")
	}
	// The project's use moved to the fork; the original is untouched.
	if got := mustUses(t, tm, "p1"); !slices.Equal(got, []dam.AssetRef{{AssetID: fork.ID, Version: 1}}) {
		t.Errorf("p1 uses %v, want only the fork", got)
	}
	if a, _ := tm.store.GetAsset(ctx, stock.ID); a.DeletedAt != nil || a.CurrentVersion != 1 {
		t.Errorf("forking changed the original: %+v", a)
	}
	// Again: the same fork, nothing new.
	if again, err := tm.ForkAsset(ctx, "member", "p1", ref, ledger.Owner{}, ""); err != nil || again.ID != fork.ID {
		t.Errorf("ForkAsset again = %+v, %v; want the same fork", again, err)
	}
	if n := len(ledgerObjects(t, tm, project, false)); n != 1 {
		t.Errorf("p1 has %d ledger objects, want 1", n)
	}

	// Into the project owner's library instead.
	lib, err := tm.ForkAsset(ctx, "member", "p1", ref, acme, "")
	if err != nil || lib.Owner != acme || lib.ProjectID != "" || lib.ID != copyOf("p1", acme, stock.ID, 1) {
		t.Errorf("ForkAsset into acme = %+v, %v; want a copy in acme's library", lib, err)
	}

	// Read directly, not used yet: guest forks p1's file into their own p3.
	own := newAssetRequest()
	own.Owner = project
	p1File, _ := uploadAsset(t, tm, "member", own)
	pref := dam.AssetRef{AssetID: p1File.ID, Version: 1}
	mine, err := tm.ForkAsset(ctx, "guest", "p3", pref, ledger.Owner{}, "")
	if err != nil || mine.Owner != (ledger.Owner{Kind: ledger.OwnerProject, ID: "p3"}) {
		t.Fatalf("guest's fork into p3 = %+v, %v", mine, err)
	}
	if got := mustUses(t, tm, "p3"); !slices.Equal(got, []dam.AssetRef{{AssetID: mine.ID, Version: 1}}) {
		t.Errorf("p3 uses %v, want the fork", got)
	}
}

func TestForkAssetRejects(t *testing.T) {
	tm, stock, _ := forkSetup(t)
	ctx := t.Context()
	ref := dam.AssetRef{AssetID: stock.ID, Version: 1}
	tm.authz.SetProjectOwner("p4", acme)
	tm.authz.AllowProject("guest", "p4", sm.AccessWrite)
	tm.authz.AllowOwner("guest", ledger.Owner{Kind: ledger.OwnerModulith}, sm.AccessRead)
	acmeFile, _ := uploadAsset(t, tm, "member", newAssetRequest()) // p1 doesn't use it; guest can't read it
	tests := []struct {
		name, actor, project string
		ref                  dam.AssetRef
		target               ledger.Owner
		want                 error
	}{
		{"project the actor only reads", "guest", "p1", ref, ledger.Owner{}, sm.ErrForbidden},
		{"asset the actor can't read", "guest", "p3", dam.AssetRef{AssetID: acmeFile.ID, Version: 1}, ledger.Owner{}, sm.ErrForbidden},
		// member writes their own library, but p1 is acme's: still not a fork target.
		{"target other than project or owner", "member", "p1", ref, ledger.Owner{Kind: ledger.OwnerUser, ID: "member"}, sm.ErrForbidden},
		// p4 is acme's and guest edits it, but guest can't write acme's library.
		{"owner's library the actor can't write", "guest", "p4", ref, acme, sm.ErrForbidden},
		{"missing version", "member", "p1", dam.AssetRef{AssetID: stock.ID, Version: 2}, ledger.Owner{}, dam.ErrVersionNotFound},
		{"version 0", "member", "p1", dam.AssetRef{AssetID: stock.ID}, ledger.Owner{}, dam.ErrInvalidQuery},
	}
	for _, tt := range tests {
		if _, err := tm.ForkAsset(ctx, tt.actor, tt.project, tt.ref, tt.target, ""); !errors.Is(err, tt.want) {
			t.Errorf("%s: ForkAsset = %v, want %v", tt.name, err, tt.want)
		}
	}
	if n := len(ledgerObjects(t, tm, project, true)); n != 0 {
		t.Errorf("refused forks left %d ledger objects in p1", n)
	}
}

// A used asset in the trash can still be forked, rescuing it before the
// purge; an unused trashed one can't be reached directly.
func TestForkTrashedAsset(t *testing.T) {
	tm, _, _ := forkSetup(t)
	ctx := t.Context()
	used, _ := uploadAsset(t, tm, "member", newAssetRequest())
	unused, _ := uploadAsset(t, tm, "member", newAssetRequest())
	usedRef := dam.AssetRef{AssetID: used.ID, Version: 1}
	if err := tm.RecordAssetUse(ctx, "member", "p1", usedRef); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	for _, id := range []string{used.ID, unused.ID} {
		if err := tm.DeleteAsset(ctx, "member", id); err != nil {
			t.Fatalf("DeleteAsset: %v", err)
		}
	}
	if _, err := tm.ForkAsset(ctx, "member", "p1", usedRef, ledger.Owner{}, ""); err != nil {
		t.Errorf("forking a used, trashed asset: %v", err)
	}
	if _, err := tm.ForkAsset(ctx, "member", "p1", dam.AssetRef{AssetID: unused.ID, Version: 1}, ledger.Owner{}, ""); !errors.Is(err, dam.ErrAssetNotFound) {
		t.Errorf("forking an unused, trashed asset = %v, want ErrAssetNotFound", err)
	}
}

// Copying out of a shared project: guest reads p1, which uses acme's logo;
// guest can't read acme's library, but can fork the logo into their own p3
// through p1's use of it.
func TestForkAssetVia(t *testing.T) {
	tm, _, _ := forkSetup(t)
	ctx := t.Context()
	logo, data := uploadAsset(t, tm, "member", newAssetRequest())
	ref := dam.AssetRef{AssetID: logo.ID, Version: 1}
	if err := tm.RecordAssetUse(ctx, "member", "p1", ref); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	// p5 (acme's) uses the logo too, but guest can't read p5; guest reads p6,
	// which doesn't use it.
	tm.authz.SetProjectOwner("p5", acme)
	tm.authz.AllowProject("member", "p5", sm.AccessWrite)
	if err := tm.RecordAssetUse(ctx, "member", "p5", ref); err != nil {
		t.Fatalf("RecordAssetUse: %v", err)
	}
	tm.authz.AllowProject("guest", "p6", sm.AccessRead)

	for _, c := range []struct{ name, via string }{
		{"without via: guest can't read acme's library", ""},
		{"via a project that uses it, which guest can't read", "p5"},
		{"via a project guest reads, which doesn't use it", "p6"},
	} {
		if _, err := tm.ForkAsset(ctx, "guest", "p3", ref, ledger.Owner{}, c.via); !errors.Is(err, sm.ErrForbidden) {
			t.Errorf("fork %s = %v, want ErrForbidden", c.name, err)
		}
	}

	fork, err := tm.ForkAsset(ctx, "guest", "p3", ref, ledger.Owner{}, "p1")
	if err != nil {
		t.Fatalf("fork via p1: %v", err)
	}
	p3 := ledger.Owner{Kind: ledger.OwnerProject, ID: "p3"}
	if fork.Owner != p3 || fork.CreatedBy != "guest" {
		t.Errorf("fork = %+v; want guest's copy owned by p3", fork)
	}
	if got := mustUses(t, tm, "p3"); !slices.Equal(got, []dam.AssetRef{{AssetID: fork.ID, Version: 1}}) {
		t.Errorf("p3 uses %v, want the fork", got)
	}
	if got := mustUses(t, tm, "p1"); len(got) != 2 || !slices.Contains(got, ref) {
		t.Errorf("forking via p1 changed p1's uses to %v (want the stock image and %s)", got, ref)
	}
	tm.authz.AllowProject("guest", "p3", sm.AccessRead) // reading the copy needs only p3
	if got, _ := download(t, tm, "guest", fork.ID, 1); !bytes.Equal(got, data) {
		t.Error("the fork's bytes aren't the logo's")
	}
}
