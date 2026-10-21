package snapshots_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
)

// The HTTP transfer reports a refused PUT itself (not only when
// CompleteSnapshot later finds nothing), and won't read past its limit.
func TestHTTPTransfer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refused":
			http.Error(w, "SignatureDoesNotMatch", http.StatusForbidden)
		case "/big":
			w.Write(make([]byte, 11))
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	tr := snapshots.NewHTTPTransfer(10)
	ctx := context.Background()

	if err := tr.Put(ctx, srv.URL+"/refused", map[string]string{"Content-Type": "x"}, []byte("data")); err == nil {
		t.Error("a 403 PUT succeeded")
	}
	if err := tr.Put(ctx, srv.URL+"/fine", nil, []byte("data")); err != nil {
		t.Errorf("a 200 PUT failed: %v", err)
	}
	if _, err := tr.Get(ctx, srv.URL+"/refused"); err == nil {
		t.Error("a 403 GET succeeded")
	}
	if _, err := tr.Get(ctx, srv.URL+"/big"); err == nil {
		t.Error("read 11 bytes with a 10-byte limit")
	}
	if got, err := tr.Get(ctx, srv.URL+"/fine"); err != nil || string(got) != "ok" {
		t.Errorf("GET = %q, %v", got, err)
	}
}
