package auth

import (
	_ "embed"
	"net/http"
)

//go:embed dev.html
var devPage []byte

// registerDevPages serves the dev-only sign-in page at /dev/signin and as
// Google's callback (/dev/callback/google): one page that handles both.
func registerDevPages(mux *http.ServeMux) {
	page := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(devPage)
	}
	mux.HandleFunc("GET /dev/signin", page)
	mux.HandleFunc("GET /dev/callback/google", page)
}
