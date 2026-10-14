package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
