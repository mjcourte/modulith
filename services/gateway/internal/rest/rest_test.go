package rest_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"gyoza/internal/engine"
)

func TestRESTMeAndOrgs(t *testing.T) {
	w := newWorld(t)
	ann, bob, eve := w.signIn("ann"), w.signIn("bob"), w.signIn("eve")

	me := w.must(http.StatusOK, ann, "GET", "/me", nil)
	if str(me.body, "user", "email") != "ann@example.com" || me.body["org"] != nil {
		t.Errorf("GET /me before any org = %s", me.raw)
	}

	org := w.must(http.StatusOK, ann, "POST", "/orgs", map[string]any{"name": "Acme"})
	orgID := str(org.body, "org", "id")
	if str(org.body, "membership", "role") != "admin" {
		t.Errorf("POST /orgs = %s, want ann as admin", org.raw)
	}
	w.must(http.StatusOK, ann, "POST", "/orgs/"+orgID+"/invites", map[string]any{"email": "bob@example.com", "role": "member"})

	// bob sees the invite on /me and accepts it.
	me = w.must(http.StatusOK, bob, "GET", "/me", nil)
	invites, _ := me.body["invites"].([]any)
	if len(invites) != 1 {
		t.Fatalf("bob's invites = %s", me.raw)
	}
	inviteID := str(invites[0], "id")
	w.must(http.StatusOK, bob, "POST", "/invites/"+inviteID+"/accept", nil)
	if members := w.must(http.StatusOK, ann, "GET", "/orgs/"+orgID+"/members", nil); len(members.list) != 2 {
		t.Errorf("members = %s, want ann and bob", members.raw)
	}
	if me = w.must(http.StatusOK, bob, "GET", "/me", nil); str(me.body, "org", "name") != "Acme" {
		t.Errorf("bob's /me after joining = %s", me.raw)
	}

	// A stranger can't see the org; a member can't rename it.
	if r := w.call(eve, "GET", "/orgs/"+orgID, nil); r.status != http.StatusForbidden || r.errorCode() != "FORBIDDEN" {
		t.Errorf("stranger GET org = %d %s, want 403 FORBIDDEN", r.status, r.raw)
	}
	if r := w.call(bob, "PATCH", "/orgs/"+orgID, map[string]any{"name": "Bob Corp"}); r.status != http.StatusForbidden {
		t.Errorf("member rename = %d %s, want 403", r.status, r.raw)
	}
	// The last admin can't leave while others remain.
	if r := w.call(ann, "DELETE", "/me/org", nil); r.status != http.StatusConflict || r.errorCode() != "LAST_ADMIN" {
		t.Errorf("last admin leaving = %d %s, want 409 LAST_ADMIN", r.status, r.raw)
	}
	if r := w.must(http.StatusOK, bob, "DELETE", "/me/org", nil); r.body["remainingMembers"] != float64(1) {
		t.Errorf("bob leaving = %s, want 1 member left", r.raw)
	}
}

func TestRESTProjectsAndShares(t *testing.T) {
	w := newWorld(t)
	ann, bob := w.signIn("ann"), w.signIn("bob")
	annLib := map[string]any{"kind": "user", "id": ann}

	p := w.must(http.StatusOK, ann, "POST", "/projects", map[string]any{"owner": annLib, "name": "Poster"})
	id := str(p.body, "id")
	if str(p.body, "owner", "id") != ann || str(p.body, "state") != "ready" {
		t.Errorf("POST /projects = %s", p.raw)
	}
	if list := w.must(http.StatusOK, ann, "GET", "/projects", nil); len(list.body["projects"].([]any)) != 1 {
		t.Errorf("GET /projects = %s", list.raw)
	}
	w.must(http.StatusOK, ann, "PATCH", "/projects/"+id, map[string]any{"name": "Flyer"})

	// bob can't see it until it's shared with him, then only as a viewer.
	if r := w.call(bob, "GET", "/projects/"+id, nil); r.status != http.StatusForbidden {
		t.Errorf("unshared GET = %d %s, want 403", r.status, r.raw)
	}
	w.must(http.StatusOK, ann, "POST", "/projects/"+id+"/shares", map[string]any{"email": "bob@example.com", "access": "viewer"})
	got := w.must(http.StatusOK, bob, "GET", "/projects/"+id, nil)
	if str(got.body, "access") != "viewer" || str(got.body, "project", "name") != "Flyer" {
		t.Errorf("shared GET = %s", got.raw)
	}
	if r := w.call(bob, "PATCH", "/projects/"+id, map[string]any{"name": "Mine"}); r.status != http.StatusForbidden {
		t.Errorf("viewer rename = %d %s, want 403", r.status, r.raw)
	}

	// bob clones it into his library: a new project he owns.
	clone := w.must(http.StatusOK, bob, "POST", "/projects/"+id+"/clone", map[string]any{"target": map[string]any{"kind": "user", "id": bob}})
	if str(clone.body, "owner", "id") != bob || str(clone.body, "clonedFrom") != id {
		t.Errorf("clone = %s", clone.raw)
	}

	w.must(http.StatusNoContent, ann, "DELETE", "/projects/"+id+"/shares/"+bob, nil)
	w.must(http.StatusNoContent, ann, "DELETE", "/projects/"+id, nil)
	if r := w.call(ann, "GET", "/projects/"+id, nil); r.status < 400 {
		t.Errorf("GET after delete = %d", r.status)
	}
	// Browsers can't name the gyoza owner (templates are service-to-service).
	if r := w.call(ann, "POST", "/projects", map[string]any{"owner": map[string]any{"kind": "gyoza"}, "name": "T"}); r.status != http.StatusBadRequest {
		t.Errorf("gyoza-owned project = %d %s, want 400", r.status, r.raw)
	}
}

// upload stores a small text file through the routes, as a browser would:
// begin, PUT to the presigned URL, complete. It returns the asset.
func (w *world) upload(actor string, req map[string]any, data string) response {
	w.t.Helper()
	req["contentType"], req["sizeBytes"] = "text/plain", len(data)
	ticket := w.must(http.StatusOK, actor, "POST", "/uploads", req)
	if err := w.files.Upload(str(ticket.body, "url"), "text/plain", []byte(data)); err != nil {
		w.t.Fatal(err)
	}
	return w.must(http.StatusOK, actor, "POST", "/uploads/"+str(ticket.body, "uploadId")+"/complete", nil)
}

func TestRESTAssetsAndTrash(t *testing.T) {
	w := newWorld(t)
	ann, eve := w.signIn("ann"), w.signIn("eve")
	a := w.upload(ann, map[string]any{"owner": map[string]any{"kind": "user", "id": ann}, "name": "notes.txt", "tags": []string{"Draft"}}, "hello")
	id := str(a.body, "id")
	if str(a.body, "category") != "text" || a.body["currentVersion"] != float64(1) {
		t.Errorf("completed upload = %s", a.raw)
	}

	list := w.must(http.StatusOK, ann, "GET", "/assets?ownerKind=user&ownerId="+ann+"&tag=draft", nil)
	if assets := list.body["assets"].([]any); len(assets) != 1 || str(assets[0], "id") != id {
		t.Errorf("GET /assets by tag = %s", list.raw)
	}
	if r := w.must(http.StatusOK, ann, "PATCH", "/assets/"+id, map[string]any{"name": "final.txt"}); str(r.body, "name") != "final.txt" {
		t.Errorf("rename = %s", r.raw)
	}
	d := w.must(http.StatusOK, ann, "GET", "/assets/"+id+"/download", nil)
	if data, err := w.files.Download(str(d.body, "url")); err != nil || string(data) != "hello" {
		t.Errorf("download = %q, %v", data, err)
	}
	if r := w.call(eve, "GET", "/assets/"+id, nil); r.status != http.StatusForbidden {
		t.Errorf("stranger GET asset = %d %s, want 403", r.status, r.raw)
	}

	w.must(http.StatusNoContent, ann, "DELETE", "/assets/"+id, nil)
	trash := w.must(http.StatusOK, ann, "GET", "/trash?ownerKind=user&ownerId="+ann, nil)
	if assets := trash.body["assets"].(map[string]any)["assets"].([]any); len(assets) != 1 || str(assets[0], "deletedAt") == "" {
		t.Errorf("trash = %s", trash.raw)
	}
	if r := w.must(http.StatusOK, ann, "POST", "/assets/"+id+"/restore", nil); r.body["deletedAt"] != nil {
		t.Errorf("restored = %s", r.raw)
	}
	if r := w.call(ann, "GET", "/assets/nope", nil); r.status != http.StatusNotFound && r.status != http.StatusForbidden {
		t.Errorf("unknown asset = %d %s", r.status, r.raw)
	}
}

func TestRESTFolders(t *testing.T) {
	w := newWorld(t)
	ann := w.signIn("ann")
	lib := map[string]any{"kind": "user", "id": ann}
	top := w.must(http.StatusOK, ann, "POST", "/folders", map[string]any{"owner": lib, "name": "Brand"})
	topID := str(top.body, "id")
	w.must(http.StatusOK, ann, "POST", "/folders", map[string]any{"owner": lib, "parentId": topID, "name": "Logos"})
	if r := w.call(ann, "POST", "/folders", map[string]any{"owner": lib, "name": "brand"}); r.status != http.StatusConflict || r.errorCode() != "NAME_TAKEN" {
		t.Errorf("duplicate name = %d %s, want 409 NAME_TAKEN", r.status, r.raw)
	}

	c := w.must(http.StatusOK, ann, "GET", "/folders?ownerKind=user&ownerId="+ann+"&folderId="+topID, nil)
	if folders := c.body["folders"].([]any); len(folders) != 1 || str(folders[0], "name") != "Logos" {
		t.Errorf("folder contents = %s", c.raw)
	}

	// A non-empty folder needs ?tree=true; the refusal says what's inside.
	r := w.call(ann, "DELETE", "/folders/"+topID, nil)
	e, _ := r.body["error"].(map[string]any)
	if r.status != http.StatusConflict || r.errorCode() != "FOLDER_NOT_EMPTY" || e["folders"] != float64(1) || e["assets"] != float64(0) {
		t.Errorf("delete non-empty = %d %s, want 409 FOLDER_NOT_EMPTY with 1 folder, 0 assets", r.status, r.raw)
	}
	if d := w.must(http.StatusOK, ann, "DELETE", "/folders/"+topID+"?tree=true", nil); d.body["folders"] != float64(2) {
		t.Errorf("tree delete = %s, want 2 folders", d.raw)
	}
	w.must(http.StatusOK, ann, "POST", "/folders/"+topID+"/restore", nil)
	if r := w.must(http.StatusOK, ann, "PATCH", "/folders/"+topID, map[string]any{"name": "Brand 2026"}); str(r.body, "name") != "Brand 2026" {
		t.Errorf("rename = %s", r.raw)
	}
}

func TestRESTDocuments(t *testing.T) {
	w := newWorld(t)
	ann, eve := w.signIn("ann"), w.signIn("eve")
	p := w.must(http.StatusOK, ann, "POST", "/projects", map[string]any{"owner": map[string]any{"kind": "user", "id": ann}, "name": "Poster"})
	pid := str(p.body, "id")

	doc := w.must(http.StatusOK, ann, "GET", "/projects/"+pid+"/document", nil)
	if doc.header.Get("Content-Type") != "application/octet-stream" || doc.header.Get("Gyoza-Format") == "" ||
		doc.header.Get("Gyoza-Seq") == "" || len(doc.raw) == 0 {
		t.Errorf("GET document: headers %v, %d bytes", doc.header, len(doc.raw))
	}
	if r := w.call(eve, "GET", "/projects/"+pid+"/document", nil); r.status != http.StatusForbidden {
		t.Errorf("stranger GET document = %d %s, want 403", r.status, r.raw)
	}

	// The document uses a library asset; forking it gives the project its own
	// copy and points the document at it.
	a := w.upload(ann, map[string]any{"owner": map[string]any{"kind": "user", "id": ann}, "name": "logo.txt"}, "logo")
	assetID := str(a.body, "id")
	in, err := engine.MockDelta("", 0, engine.MockSet{Key: "logo", Value: engine.AssetRef{ID: assetID, Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.docs.UpdateDocument(context.Background(), ann, pid, in); err != nil {
		t.Fatal(err)
	}
	fork := w.must(http.StatusOK, ann, "POST", "/projects/"+pid+"/forks", map[string]any{"ref": map[string]any{"assetId": assetID, "version": 1}})
	forkID := str(fork.body, "asset", "id")
	if str(fork.body, "asset", "owner", "kind") != "project" || fork.body["rewriteError"] != nil {
		t.Errorf("fork = %s", fork.raw)
	}
	q := w.must(http.StatusOK, ann, "POST", "/projects/"+pid+"/document/query", map[string]any{"format": "native"})
	if data := string(q.raw); !strings.Contains(data, forkID) || strings.Contains(data, assetID) {
		t.Errorf("document after the fork = %s, want it using %s only", q.raw, forkID)
	}
	ref := w.must(http.StatusOK, ann, "GET", "/projects/"+pid+"/refs/"+forkID+"/1", nil)
	if str(ref.body, "asset", "id") != forkID || str(ref.body, "download", "url") == "" {
		t.Errorf("resolve ref = %s", ref.raw)
	}

	// Snapshot downloads: storage doesn't authorize them, the gateway does.
	latest, err := w.files.LatestSnapshot(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	snapPath := "/projects/" + pid + "/snapshots/" + latest.ObjectID
	if r := w.call(eve, "GET", snapPath, nil); r.status != http.StatusForbidden {
		t.Errorf("stranger snapshot download = %d %s, want 403", r.status, r.raw)
	}
	if s := w.must(http.StatusOK, ann, "GET", snapPath, nil); str(s.body, "url") == "" {
		t.Errorf("snapshot download = %s", s.raw)
	}
}

// Changing requests must be JSON (the CSRF defence); reads needn't be.
func TestRESTRefusesNonJSONChanges(t *testing.T) {
	w := newWorld(t)
	ann := w.signIn("ann")
	req, _ := http.NewRequest("POST", "/orgs", strings.NewReader(`{"name":"Acme"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set(actorHeader, ann)
	rec := w.send(req)
	if rec.status != http.StatusUnsupportedMediaType || rec.errorCode() != "NOT_JSON" {
		t.Errorf("text/plain POST = %d %s, want 415 NOT_JSON", rec.status, rec.raw)
	}
	if me, _ := w.iam.GetMe(context.Background(), ann); me.Org != nil {
		t.Error("the refused request created an org")
	}
}

// A request without an actor (a route mounted outside the session by
// mistake) is refused by the services.
func TestRESTWithoutActorIsRefused(t *testing.T) {
	w := newWorld(t)
	if r := w.call("", "GET", "/me", nil); r.status < 400 || r.status >= 500 {
		t.Errorf("GET /me without an actor = %d %s, want a 4xx", r.status, r.raw)
	}
}
