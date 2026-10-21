package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	gsession "github.com/mjcourte/modulith/services/gateway/internal/session"
)

// Every /api route is behind a session: without one the API is never
// reached. Needs the SuperTokens core (MODULITH_TEST_SUPERTOKENS_URL); Init's
// state is global, so this is the package's only Init.
func TestAPIRequiresASession(t *testing.T) {
	url := os.Getenv("MODULITH_TEST_SUPERTOKENS_URL")
	if url == "" {
		t.Skip("set MODULITH_TEST_SUPERTOKENS_URL to run against the SuperTokens core")
	}
	reached := false
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if gsession.Actor(r.Context()) == "" {
			t.Error("the API was reached without an actor")
		}
	})
	h, err := Init(Config{
		CoreURL: url, PublicURL: "http://localhost:8080",
		GoogleClientID: "test-client", GoogleClientSecret: "test-secret",
	}, &fakeUsers{}, api)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/me", "/api/projects", "/api/anything"} {
		reached = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized || reached {
			t.Errorf("GET %s without a session = %d (API reached: %v), want 401 and not reached", path, rec.Code, reached)
		}
	}
	// A forged access-token cookie is no session either.
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "sAccessToken", Value: "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbm4ifQ."})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || reached {
		t.Errorf("forged token = %d (API reached: %v), want 401 and not reached", rec.Code, reached)
	}
	// The dev pages exist only when asked for.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/dev/signin", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /dev/signin without DevPages = %d, want 404", rec.Code)
	}
}
