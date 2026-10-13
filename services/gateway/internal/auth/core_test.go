package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Against the compose core (GYOZA_TEST_SUPERTOKENS_URL): deleting a user it
// doesn't know succeeds, so a resumed account deletion can repeat it.
func TestCoreIdentityProviderDeletesMissingUser(t *testing.T) {
	url := os.Getenv("GYOZA_TEST_SUPERTOKENS_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_SUPERTOKENS_URL to run against the SuperTokens core")
	}
	p := NewCoreIdentityProvider(url, os.Getenv("GYOZA_TEST_SUPERTOKENS_API_KEY"))
	if err := p.DeleteUser(context.Background(), "gyoza-test-no-such-user"); err != nil {
		t.Fatalf("DeleteUser of a missing user: %v", err)
	}
}

// A refusal from the core is an error, never a silent success.
func TestCoreIdentityProviderReportsRefusals(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"bad API key": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Invalid API key", http.StatusUnauthorized)
		},
		"not OK": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"UNKNOWN"}`)) },
	} {
		srv := httptest.NewServer(h)
		if err := NewCoreIdentityProvider(srv.URL, "").DeleteUser(context.Background(), "u"); err == nil {
			t.Errorf("%s: DeleteUser succeeded", name)
		}
		srv.Close()
	}
}
