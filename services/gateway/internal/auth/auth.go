// Package auth is the gateway's authentication: SuperTokens sign-in (Google
// only) and cookie sessions, the hook that records each sign-in in iam, and
// iam's IdentityProvider. No other service sees cookies or credentials.
//
//   - auth.go: Config, Init (the SDK setup) and the HTTP handler
//   - hook.go: the sign-in hook (Users, identityFrom)
//   - core.go: CoreIdentityProvider, deleting accounts through the core
//   - dev.go, dev.html: dev-only sign-in pages, until apps/web exists
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/supertokens/supertokens-golang/recipe/session"
	"github.com/supertokens/supertokens-golang/recipe/session/sessmodels"
	"github.com/supertokens/supertokens-golang/recipe/thirdparty"
	"github.com/supertokens/supertokens-golang/recipe/thirdparty/tpmodels"
	"github.com/supertokens/supertokens-golang/supertokens"

	gsession "gyoza/services/gateway/internal/session"
)

// Config is what authentication needs.
type Config struct {
	CoreURL, CoreAPIKey string
	// PublicURL is where browsers reach the gateway: its API and, until
	// apps/web is served elsewhere, the website (sign-in pages, callback).
	PublicURL                          string
	GoogleClientID, GoogleClientSecret string
	// DevPages serves the dev-only sign-in pages under /dev/.
	DevPages bool
}

var initialised atomic.Bool

// Init sets up the SuperTokens SDK and returns the gateway's handler:
// SuperTokens' /auth/* routes, api under /api/ behind a session (Require),
// and with DevPages the sign-in pages. The SDK keeps global state, so Init
// may run once per process.
func Init(cfg Config, users Users, api http.Handler) (http.Handler, error) {
	if !initialised.CompareAndSwap(false, true) {
		return nil, errors.New("gateway: auth.Init called twice")
	}
	err := supertokens.Init(supertokens.TypeInput{
		Supertokens: &supertokens.ConnectionInfo{ConnectionURI: cfg.CoreURL, APIKey: cfg.CoreAPIKey},
		AppInfo: supertokens.AppInfo{
			AppName:       "Gyoza",
			APIDomain:     cfg.PublicURL,
			WebsiteDomain: cfg.PublicURL,
		},
		RecipeList: []supertokens.Recipe{
			thirdparty.Init(&tpmodels.TypeInput{
				SignInAndUpFeature: tpmodels.TypeInputSignInAndUp{
					Providers: []tpmodels.ProviderInput{{Config: tpmodels.ProviderConfig{
						ThirdPartyId: "google",
						Clients: []tpmodels.ProviderClientConfig{{
							ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret,
							// The SDK's default is openid and email only: profile
							// adds the name and picture iam records.
							Scope: []string{"openid", "email", "profile"},
						}},
					}}},
				},
				Override: &tpmodels.OverrideStruct{Functions: signInHook(users)},
			}),
			session.Init(&sessmodels.TypeInput{
				// Browsers only: always cookies (httpOnly), never tokens in
				// headers that page scripts could read.
				GetTokenTransferMethod: func(*http.Request, bool, supertokens.UserContext) sessmodels.TokenTransferMethod {
					return sessmodels.CookieTransferMethod
				},
			}),
		},
	})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", Require(http.StripPrefix("/api", api)))
	if cfg.DevPages {
		registerDevPages(mux)
	}
	// Middleware answers SuperTokens' own routes (/auth/...) and passes the
	// rest to mux.
	return supertokens.Middleware(mux), nil
}

// Require lets only requests with a valid session reach next, with the
// session's user as the actor (session.Actor). Others get SuperTokens'
// 401 (try refreshing) or its error response.
func Require(next http.Handler) http.Handler {
	return session.VerifySession(nil, func(w http.ResponseWriter, r *http.Request) {
		sess := session.GetSessionFromRequestContext(r.Context())
		ctx := gsession.WithActor(r.Context(), sess.GetUserID())
		if exp, ok := accessExpiry(sess.GetAccessTokenPayload()); ok {
			ctx = gsession.WithExpiry(ctx, exp)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// accessExpiry reads the access token's exp claim (seconds since the epoch).
// The payload is decoded JSON, so numbers usually arrive as float64.
func accessExpiry(payload map[string]any) (time.Time, bool) {
	var secs int64
	switch v := payload["exp"].(type) {
	case float64:
		secs = int64(v)
	case int64:
		secs = v
	case uint64:
		secs = int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return time.Time{}, false
		}
		secs = n
	default:
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}
