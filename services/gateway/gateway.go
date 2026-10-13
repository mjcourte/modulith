// Package gateway is the single entry point from the browser: SuperTokens
// sign-in and sessions, the JSON API (/api) and the WebSocket (/api/ws).
// Other services see only the actor it passes them, never cookies or
// tokens.
package gateway

import (
	"context"
	"net/http"

	gnats "gyoza/platform/nats"
	"gyoza/services/documents"
	"gyoza/services/gateway/internal/auth"
	"gyoza/services/gateway/internal/rest"
	"gyoza/services/gateway/internal/ws"
	"gyoza/services/iam"
	"gyoza/services/storage"
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
