package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gyoza/services/documents"
	"gyoza/services/gateway/internal/rest"
	"gyoza/services/gateway/internal/session"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// world is the monolith in memory behind the REST handler: real iam,
// storage and documents (documents without NATS). Requests name their actor
// in a test header, standing in for the session.
type world struct {
	t     *testing.T
	h     http.Handler
	iam   *iam.Manager
	files *storage.Memory
	docs  *documents.Manager
}

const actorHeader = "X-Test-Actor"

func newWorld(t *testing.T) *world {
	t.Helper()
	store := iam.NewMemoryStore()
	authz := iam.NewStorageAuthorizer(store)
	files, err := storage.NewMemory(authz, storage.Config{Bucket: "gyoza-test"})
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.OpenLocal(files, memTransfer{files}, authz)
	t.Cleanup(func() { docs.Close(context.Background()) })
	m, err := iam.NewManager(store, files, docs, iam.NoIdentityProvider{}, iam.Config{})
	if err != nil {
		t.Fatal(err)
	}
	api := rest.New(m, files, docs, authz).Handler()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r.WithContext(session.WithActor(r.Context(), r.Header.Get(actorHeader))))
	})
	return &world{t: t, h: h, iam: m, files: files, docs: docs}
}

// memTransfer moves snapshot bytes through storage.Memory's fake S3.
type memTransfer struct{ m *storage.Memory }

func (t memTransfer) Put(_ context.Context, url string, h map[string]string, data []byte) error {
	return t.m.Upload(url, h["Content-Type"], data)
}

func (t memTransfer) Get(_ context.Context, url string) ([]byte, error) { return t.m.Download(url) }

// signIn creates a user as the gateway's sign-in hook would, with a verified
// email name@example.com, and returns their ID.
func (w *world) signIn(name string) string {
	w.t.Helper()
	_, err := w.iam.SignIn(context.Background(), iam.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true, Name: name})
	if err != nil {
		w.t.Fatal(err)
	}
	return name
}

// response is a decoded reply: its status, and its JSON body as generic
// values (body) and raw (raw), to decode into a struct when convenient.
type response struct {
	status int
	header http.Header
	body   map[string]any
	list   []any
	raw    []byte
}

// errorCode is the error response's code, or "".
func (r response) errorCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// call sends a request as actor; a non-nil body is sent as JSON.
func (w *world) call(actor, method, path string, body any) response {
	w.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			w.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(actorHeader, actor)
	return w.send(req)
}

// send serves a prepared request (actorHeader set by the caller).
func (w *world) send(req *http.Request) response {
	w.t.Helper()
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	out := response{status: rec.Code, header: rec.Header(), raw: rec.Body.Bytes()}
	if rec.Header().Get("Content-Type") == "application/json" {
		var v any
		if err := json.Unmarshal(out.raw, &v); err != nil {
			w.t.Fatalf("%s %s: response isn't JSON: %v: %s", req.Method, req.URL, err, out.raw)
		}
		out.body, _ = v.(map[string]any)
		out.list, _ = v.([]any)
	}
	return out
}

// must calls and fails the test unless the status is want.
func (w *world) must(want int, actor, method, path string, body any) response {
	w.t.Helper()
	r := w.call(actor, method, path, body)
	if r.status != want {
		w.t.Fatalf("%s %s as %s = %d %s, want %d", method, path, actor, r.status, r.raw, want)
	}
	return r
}

// str reads a string at a path of keys in a decoded body.
func str(v any, keys ...string) string {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	s, _ := v.(string)
	return s
}
