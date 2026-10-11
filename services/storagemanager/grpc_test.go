package storagemanager_test

import (
	"context"
	"errors"
	"net"
	"reflect"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "gyoza/contracts/gen/go/gyoza/storagemanager/v1"
	"gyoza/services/storageledger"
	sm "gyoza/services/storagemanager"
)

// newGRPCClient serves svc over an in-memory connection and returns a client
// for it: real gRPC encoding and status handling, no network.
func newGRPCClient(t *testing.T, svc sm.Service) *sm.GRPCClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterStorageManagerServiceServer(srv, sm.NewGRPCServer(svc))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return sm.NewGRPCClient(conn)
}

// norm makes values that mean the same compare equal: protobuf can't tell an
// empty list from a missing one, so empty slices become nil.
func norm[T any](v T) T {
	normValue(reflect.ValueOf(&v).Elem())
	return v
}

func normValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.Slice:
		if v.Len() == 0 {
			v.SetZero()
			return
		}
		for i := range v.Len() {
			normValue(v.Index(i))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				normValue(v.Field(i))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			normValue(v.Elem())
		}
	}
}

// same fails the test if got (through gRPC) differs from want (direct).
func same[T any](t *testing.T, what string, got T, gotErr error, want T, wantErr error) {
	t.Helper()
	if gotErr != nil || wantErr != nil {
		t.Fatalf("%s: over gRPC %v, direct %v", what, gotErr, wantErr)
	}
	if !reflect.DeepEqual(norm(got), norm(want)) {
		t.Errorf("%s over gRPC:\n got %+v\nwant %+v", what, got, want)
	}
}

// Every call, through the client, returns what the Manager itself returns.
// The Manager sits behind the server, so the adapters are what's under test.
func TestGRPCRoundTrip(t *testing.T) {
	tm := newTestManager(t)
	c := newGRPCClient(t, tm.Manager)
	ctx := t.Context()

	// Folders.
	brand, err := c.CreateFolder(ctx, "member", sm.Folder{Owner: acme, Name: "Brand"})
	got, gerr := tm.store.GetFolder(ctx, brand.ID)
	same(t, "CreateFolder", brand, err, got, gerr)
	logos, err := c.CreateFolder(ctx, "member", sm.Folder{Owner: acme, Name: "Logos", ParentID: brand.ID})
	if err != nil || logos.ParentID != brand.ID {
		t.Fatalf("CreateFolder(Logos) = %+v, %v", logos, err)
	}

	// Upload a new asset, then a second version, all through the client.
	req := newAssetRequest()
	req.FolderID, req.Description = logos.ID, "the logo"
	ticket, err := c.BeginUpload(ctx, "member", req)
	if err != nil || ticket.Method != "PUT" || ticket.Headers["Content-Type"] != "image/png" || time.Until(ticket.ExpiresAt) <= 0 {
		t.Fatalf("BeginUpload = %+v, %v", ticket, err)
	}
	if err := tm.blobs.PutSigned(ticket.URL, "image/png", fileOf("image/png", 2048)); err != nil {
		t.Fatalf("browser upload: %v", err)
	}
	a, err := c.CompleteUpload(ctx, "member", ticket.UploadID)
	direct, derr := tm.GetAsset(ctx, "member", a.ID)
	same(t, "CompleteUpload", a, err, direct, derr)
	if a.Category != sm.CategoryImage || !slices.Equal(a.Tags, []string{"brand"}) || a.CreatedAt.IsZero() {
		t.Errorf("asset over gRPC lost fields: %+v", a)
	}
	ticket2, err := c.BeginUpload(ctx, "member", sm.UploadRequest{AssetID: a.ID, ContentType: "image/png", SizeBytes: 1024})
	if err != nil {
		t.Fatalf("BeginUpload(version 2): %v", err)
	}
	tm.blobs.PutSigned(ticket2.URL, "image/png", fileOf("image/png", 1024))
	if a, err = c.CompleteUpload(ctx, "member", ticket2.UploadID); err != nil || a.CurrentVersion != 2 {
		t.Fatalf("CompleteUpload(version 2) = %+v, %v", a, err)
	}

	vs, err := c.ListVersions(ctx, "member", a.ID)
	dvs, derr := tm.ListVersions(ctx, "member", a.ID)
	same(t, "ListVersions", vs, err, dvs, derr)
	reverted, err := c.RevertAsset(ctx, "member", a.ID, 1)
	if err != nil || reverted.CurrentVersion != 1 {
		t.Errorf("RevertAsset = %+v, %v", reverted, err)
	}

	d, err := c.DownloadURL(ctx, "member", a.ID, 2)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	if data, name, err := tm.blobs.GetSigned(d.URL); err != nil || len(data) != 1024 || name != d.Filename || d.Filename != "Logo.png" {
		t.Errorf("download = %d bytes as %q (ticket says %q), %v", len(data), name, d.Filename, err)
	}

	// Partial updates: unset fields stay as they are; set-to-empty clears.
	renamed, err := c.UpdateAsset(ctx, "member", a.ID, sm.AssetUpdate{Name: ptr("Mark")})
	if err != nil || renamed.Name != "Mark" || renamed.Description != "the logo" || len(renamed.Tags) != 1 || renamed.FolderID != logos.ID {
		t.Errorf("UpdateAsset(name only) = %+v, %v", renamed, err)
	}
	cleared, err := c.UpdateAsset(ctx, "member", a.ID, sm.AssetUpdate{Tags: ptr([]string{}), FolderID: ptr("")})
	if err != nil || len(cleared.Tags) != 0 || cleared.FolderID != "" || cleared.Description != "the logo" {
		t.Errorf("UpdateAsset(clear tags, move to top) = %+v, %v", cleared, err)
	}

	// Listing: an unset folder filter means any folder, "" the top level.
	for name, q := range map[string]sm.AssetQuery{
		"any folder": {Owner: acme},
		"top level":  {Owner: acme, Folder: ptr("")},
		"in Logos":   {Owner: acme, Folder: ptr(logos.ID)},
		"images":     {Owner: acme, Category: sm.CategoryImage, PageSize: 1},
	} {
		page, err := c.ListAssets(ctx, "member", q)
		dpage, derr := tm.ListAssets(ctx, "member", q)
		same(t, "ListAssets "+name, page, err, dpage, derr)
	}
	if page, _ := c.ListAssets(ctx, "member", sm.AssetQuery{Owner: acme, Folder: ptr(logos.ID)}); len(page.Assets) != 0 {
		t.Errorf("folder filter lost on the way: %d assets in the now-empty Logos", len(page.Assets))
	}

	// Folder views.
	contents, err := c.ListFolderContents(ctx, "member", acme, brand.ID, "")
	dcontents, derr := tm.ListFolderContents(ctx, "member", acme, brand.ID, "")
	same(t, "ListFolderContents", contents, err, dcontents, derr)
	path, err := c.FolderPath(ctx, "member", logos.ID)
	dpath, derr := tm.FolderPath(ctx, "member", logos.ID)
	same(t, "FolderPath", path, err, dpath, derr)
	if f, err := c.RenameFolder(ctx, "member", logos.ID, "Marks"); err != nil || f.Name != "Marks" {
		t.Errorf("RenameFolder = %+v, %v", f, err)
	}
	archive, _ := c.CreateFolder(ctx, "member", sm.Folder{Owner: acme, Name: "Archive"})
	if f, err := c.MoveFolder(ctx, "member", logos.ID, archive.ID); err != nil || f.ParentID != archive.ID {
		t.Errorf("MoveFolder into Archive = %+v, %v", f, err)
	}

	// Errors keep their identity, details included.
	c.UpdateAsset(ctx, "member", a.ID, sm.AssetUpdate{FolderID: ptr(brand.ID)})
	_, err = c.DeleteFolder(ctx, "member", brand.ID)
	var notEmpty *sm.FolderNotEmptyError
	if !errors.As(err, &notEmpty) || notEmpty.Assets != 1 {
		t.Errorf("DeleteFolder(not empty) = %v, want FolderNotEmptyError with 1 asset", err)
	}
	if _, err := c.GetAsset(ctx, "member", "nope"); !errors.Is(err, sm.ErrAssetNotFound) {
		t.Errorf("GetAsset(missing) = %v, want ErrAssetNotFound", err)
	}
	if _, err := c.GetAsset(ctx, "stranger", a.ID); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("GetAsset(stranger) = %v, want ErrForbidden", err)
	}
	if _, err := c.CreateFolder(ctx, "member", sm.Folder{Owner: acme, Name: "BRAND"}); !errors.Is(err, sm.ErrNameTaken) {
		t.Errorf("CreateFolder(clash) = %v, want ErrNameTaken", err)
	}

	// Trash: delete the tree, list it, restore it.
	del, err := c.DeleteFolderTree(ctx, "member", brand.ID)
	if err != nil || del.Folders != 1 || del.Assets != 1 || del.DeletionID == "" {
		t.Fatalf("DeleteFolderTree = %+v, %v", del, err)
	}
	trash, err := c.ListTrash(ctx, "member", acme, "")
	dtrash, derr := tm.ListTrash(ctx, "member", acme, "")
	same(t, "ListTrash", trash, err, dtrash, derr)
	if restored, err := c.RestoreFolder(ctx, "member", brand.ID); err != nil || restored.DeletionID != del.DeletionID {
		t.Errorf("RestoreFolder = %+v, %v; want deletion %s back", restored, err, del.DeletionID)
	}
	if err := c.DeleteAsset(ctx, "member", a.ID); err != nil {
		t.Errorf("DeleteAsset: %v", err)
	}
	if r, err := c.RestoreAsset(ctx, "member", a.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("RestoreAsset = %+v, %v", r, err)
	}

	// Exports.
	export, _ := tm.ledger.Create(ctx, storageledger.Object{Owner: acme, Kind: storageledger.KindExport,
		Bucket: testBucket, Key: "library/export/x", SizeBytes: 3, ContentType: "video/mp4"})
	if d, err := c.ExportURL(ctx, "member", export.ID); err != nil || d.URL == "" {
		t.Errorf("ExportURL = %+v, %v", d, err)
	}
	if _, err := c.ExportURL(ctx, "member", "nope"); !errors.Is(err, sm.ErrExportNotFound) {
		t.Errorf("ExportURL(missing) = %v, want ErrExportNotFound", err)
	}
}

// The owner lifecycle reports come through whole.
func TestGRPCOwnerLifecycle(t *testing.T) {
	tm := newTestManager(t)
	c := newGRPCClient(t, tm.Manager)
	ctx := t.Context()
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	own := newAssetRequest()
	own.Owner = project
	uploadAsset(t, tm, "member", own)

	del, err := c.DeleteOwned(ctx, project)
	if err != nil || del.LedgerObjects != 1 || del.Trash.Assets != 1 || del.Trash.DeletionID == "" {
		t.Errorf("DeleteOwned = %+v, %v", del, err)
	}

	bobs := newAssetRequest()
	bobs.Owner = storageledger.Owner{Kind: storageledger.OwnerUser, ID: "bob"}
	uploadAsset(t, tm, "bob", bobs)
	moved, err := c.RemoveUser(ctx, "bob", "acme")
	want := sm.UserRemoval{TransferredTo: acme, Transfer: sm.OwnerTransfer{Assets: 1}, LedgerObjects: 1, ClearedLedger: 1, ClearedDAM: 2}
	if err != nil || moved != want {
		t.Errorf("RemoveUser(with org) = %+v, %v; want %+v", moved, err, want)
	}

	if _, err := c.RemoveUser(ctx, "", ""); !errors.Is(err, sm.ErrInvalidQuery) {
		t.Errorf("RemoveUser(no user) = %v, want ErrInvalidQuery", err)
	}
}
