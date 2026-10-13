// Package rest is the gateway's JSON API for browsers (/api/...): one route
// per service call, with the signed-in user as the actor. It owns its wire
// types, so a service's fields reach a browser only through a deliberate
// conversion.
//
//   - server.go: Server, routes, the request adapter and the JSON-only rule
//   - json.go: decoding, encoding and the shared wire types (owners, pages)
//   - errors.go: the error tables (sentinel -> status and code)
//   - rest_*.go: one file per area (me, orgs, projects, assets, folders,
//     documents)
package rest

import (
	"errors"
	"mime"
	"net/http"

	"gyoza/services/documents"
	"gyoza/services/gateway/internal/session"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// Server answers /api routes by calling the services, which may be in
// process or gRPC clients: the handlers can't tell.
type Server struct {
	iam   iam.Service
	files storage.Service
	docs  documents.Service
	// authz is iam's answer to storage's questions, for the calls storage
	// doesn't authorize itself (its snapshot calls take no actor).
	authz storage.Authorizer
}

func New(iamSvc iam.Service, files storage.Service, docs documents.Service, authz storage.Authorizer) *Server {
	return &Server{iam: iamSvc, files: files, docs: docs, authz: authz}
}

// Handler returns the routes, relative to /api (mount it with
// http.StripPrefix). They expect session.Actor to be set: mount them behind
// auth.Require.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routesMe(mux)
	s.routesOrgs(mux)
	s.routesProjects(mux)
	s.routesAssets(mux)
	s.routesFolders(mux)
	s.routesDocuments(mux)
	return jsonOnly(mux)
}

// call is what most handlers are: the actor and the request in, a value to
// encode (or nil for 204 No Content) or an error out.
type call func(r *http.Request, actor string) (any, error)

// serve adapts a call to an http.Handler.
func serve(fn call) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r, session.Actor(r.Context()))
		if err != nil {
			writeError(w, r, err)
			return
		}
		if v == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

// jsonOnly refuses changing requests that aren't JSON. With SameSite=Lax
// cookies and no CORS, this is the CSRF defence: a cross-site page can't send
// Content-Type: application/json without a preflight, which fails.
func jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				writeError(w, r, errNotJSON)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

var errNotJSON = errors.New("gateway: requests that change something must be application/json")
