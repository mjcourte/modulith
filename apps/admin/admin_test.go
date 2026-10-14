package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"gyoza/services/iam"
)

// The admin UI has no authentication, so other sites must not reach it:
// neither by a cross-site form post nor through DNS rebinding.
func TestGuardKeepsOtherSitesOut(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tests := []struct {
		name         string
		method, host string
		header       map[string]string
		want         int
	}{
		{"same-origin post", "POST", "localhost:8081", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"loopback IP", "POST", "127.0.0.1:8081", nil, http.StatusOK},
		{"IPv6 loopback", "GET", "[::1]:8081", nil, http.StatusOK},
		{"cross-site post", "POST", "localhost:8081", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"cross-origin post, older browser", "POST", "localhost:8081", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"rebound host name", "GET", "evil.example:8081", nil, http.StatusForbidden},
		{"rebound host name, same-origin post", "POST", "evil.example:8081", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "/uses", nil)
		req.Host = tt.host
		for k, v := range tt.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, rec.Code, tt.want)
		}
	}
}

// orgStub answers the org calls the redirect tests make; any other
// iam.Service method panics on the nil embedded interface.
type orgStub struct {
	iam.Service
	remaining int
	err       error
}

func (s orgStub) RemoveMember(context.Context, string, string, string) (int, error) {
	return s.remaining, s.err
}

func (s orgStub) DeleteOrg(context.Context, string, string) error { return s.err }

// After a change on an org's page, the redirect must not land on that page
// once the org is gone (it would be a bare 404).
func TestOrgRedirectsSkipDeletedOrg(t *testing.T) {
	failed := errors.New("storage unavailable")
	tests := []struct {
		name     string
		path     string
		stub     orgStub
		wantPath string
		wantErr  bool
	}{
		{"remove a member, others remain", "/orgs/o1/members/u2/remove", orgStub{remaining: 1}, "/orgs/o1", false},
		{"remove the last member", "/orgs/o1/members/u1/remove", orgStub{remaining: 0}, "/orgs", false},
		{"remove refused", "/orgs/o1/members/u1/remove", orgStub{err: failed}, "/orgs/o1", true},
		{"delete the org", "/orgs/o1/delete", orgStub{}, "/orgs", false},
		{"delete failed partway", "/orgs/o1/delete", orgStub{err: failed}, "/orgs", true},
	}
	for _, tt := range tests {
		s := &server{iam: tt.stub}
		req := httptest.NewRequest("POST", tt.path, nil)
		req.Host = "localhost:8081" // guard
		req.Header.Set("Referer", "http://localhost:8081/orgs/o1")
		req.AddCookie(&http.Cookie{Name: actorCookie, Value: "u1"})
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, req)

		loc, err := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || err != nil {
			t.Errorf("%s: status %d, Location %q", tt.name, rec.Code, rec.Header().Get("Location"))
			continue
		}
		if loc.Path != tt.wantPath {
			t.Errorf("%s: redirected to %s, want %s", tt.name, loc.Path, tt.wantPath)
		}
		if gotErr := loc.Query().Get("err") == "1"; gotErr != tt.wantErr {
			t.Errorf("%s: error flash %v, want %v (flash %q)", tt.name, gotErr, tt.wantErr, loc.Query().Get("flash"))
		}
	}
}
