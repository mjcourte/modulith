package gateway

// Setup shared by the binaries hosting the gateway (cmd/modulith,
// cmd/gateway), so they can't drift apart.

import (
	"github.com/mjcourte/modulith/platform/config"
	"github.com/mjcourte/modulith/services/gateway/internal/auth"
	"github.com/mjcourte/modulith/services/iam"
)

// Settings is everything the gateway reads from the environment.
type Settings struct {
	Addr string // the HTTP listener
	// NATSURL is the cluster carrying live document traffic (comma-separated
	// members). cmd/gateway connects to it; the monolith shares its own
	// connection.
	NATSURL string
	Auth    auth.Config
}

// SettingsFromEnv reads the gateway's settings. Problems are recorded in env;
// check env.Err() after reading everything.
func SettingsFromEnv(env *config.Env) Settings {
	coreURL, coreKey := coreFromEnv(env)
	return Settings{
		// localhost by default so local runs don't listen on the network.
		Addr:    env.String("MODULITH_GATEWAY_ADDR", "localhost:8080"),
		NATSURL: env.Required("MODULITH_NATS_URL"),
		Auth: auth.Config{
			CoreURL:    coreURL,
			CoreAPIKey: coreKey,
			// Where browsers reach the gateway; session cookies are scoped to it.
			PublicURL:          env.String("MODULITH_PUBLIC_URL", "http://localhost:8080"),
			GoogleClientID:     env.Required("MODULITH_GOOGLE_CLIENT_ID"),
			GoogleClientSecret: env.Required("MODULITH_GOOGLE_CLIENT_SECRET"),
			DevPages:           env.Bool("MODULITH_GATEWAY_DEV_PAGES", false),
		},
	}
}

// IdentityProviderFromEnv is iam's IdentityProvider: deleting an account
// also deletes its sign-in account in the SuperTokens core. It needs only
// the core's address (no Open, no Google keys), so cmd/iam can use it.
func IdentityProviderFromEnv(env *config.Env) iam.IdentityProvider {
	return auth.NewCoreIdentityProvider(coreFromEnv(env))
}

func coreFromEnv(env *config.Env) (url, apiKey string) {
	return env.String("MODULITH_SUPERTOKENS_URL", "http://localhost:3567"), env.String("MODULITH_SUPERTOKENS_API_KEY", "")
}
