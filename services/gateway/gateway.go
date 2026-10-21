// Package gateway is the single entry point from the browser: SuperTokens
// sign-in and sessions, the JSON API (/api) and the WebSocket (/api/ws).
// Other services see only the actor it passes them, never cookies or
// tokens.
package gateway

import (
	"context"
	"net/http"

	gnats "github.com/mjcourte/modulith/platform/nats"
	"github.com/mjcourte/modulith/platform/telemetry"
	"github.com/mjcourte/modulith/services/documents"
	"github.com/mjcourte/modulith/services/gateway/internal/auth"
	"github.com/mjcourte/modulith/services/gateway/internal/rest"
	"github.com/mjcourte/modulith/services/gateway/internal/ws"
	"github.com/mjcourte/modulith/services/iam"
	"github.com/mjcourte/modulith/services/storage"
)

// Services is what the gateway calls: in-process implementations in the
// monolith, gRPC clients when distributed.
type Services struct {
	IAM       iam.Service
	Storage   storage.Service
	Documents documents.Service
	// Authz is iam's answer to storage's permission questions, for the calls
	// storage doesn't authorize itself (snapshot downloads, WebSocket edits).
	Authz storage.Authorizer
	// NATS carries the live document traffic the WebSocket relays.
	NATS *gnats.Client
}

// Gateway serves browsers. Handler answers HTTP; StartFeed must run for
// WebSockets to receive live traffic.
type Gateway struct {
	Handler http.Handler
	hub     *ws.Hub
}

// Open sets up authentication and the routes. It initialises the
// SuperTokens SDK, whose state is global: call it once per process.
func Open(s Settings, svc Services) (*Gateway, error) {
	hub := ws.New(svc.Documents, svc.Authz, ws.NewNATSBus(svc.NATS))
	api := http.NewServeMux()
	api.Handle("GET /ws", hub.Handler())
	api.Handle("/", rest.New(svc.IAM, svc.Storage, svc.Documents, svc.Authz).Handler())
	handler, err := auth.Init(s.Auth, svc.IAM, api)
	if err != nil {
		return nil, err
	}
	// One span per request, outermost so sign-in (/auth) is traced too. Not
	// the WebSocket upgrade: its messages get their own spans (internal/ws).
	handler = telemetry.HTTPHandler(handler, func(r *http.Request) bool { return r.URL.Path == "/api/ws" })
	return &Gateway{Handler: handler, hub: hub}, nil
}

// StartFeed relays every project's live traffic (one NATS consumer for the
// whole instance) to the WebSockets until ctx is done, then closes them. The
// returned channel closes once it has stopped.
func (g *Gateway) StartFeed(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.hub.Run(ctx)
	}()
	return done
}
