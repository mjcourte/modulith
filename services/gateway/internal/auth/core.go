package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gyoza/platform/telemetry"
	"gyoza/services/iam"
)

// coreAPIVersion is the core driver interface the SDK speaks (its
// coreDriverInterfaceSupported.json); requests here use the same.
const coreAPIVersion = "3.1"

// CoreIdentityProvider is iam's IdentityProvider: it deletes sign-in
// accounts through the SuperTokens core's HTTP API. It needs no SDK
// initialisation, so cmd/iam can use it without hosting the gateway.
type CoreIdentityProvider struct {
	URL    string // the core, e.g. http://localhost:3567
	APIKey string // empty if the core has none
	Client *http.Client
}

var _ iam.IdentityProvider = (*CoreIdentityProvider)(nil)

func NewCoreIdentityProvider(url, apiKey string) *CoreIdentityProvider {
	return &CoreIdentityProvider{URL: strings.TrimRight(url, "/"), APIKey: apiKey, Client: &http.Client{Timeout: 10 * time.Second, Transport: telemetry.HTTPTransport(nil)}}
}

// DeleteUser removes the account with its sessions. The core answers OK for
// a user it doesn't know, so a resumed account deletion can repeat it.
func (p *CoreIdentityProvider) DeleteUser(ctx context.Context, userID string) error {
	body, _ := json.Marshal(map[string]string{"userId": userID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/user/remove", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("cdi-version", coreAPIVersion)
	if p.APIKey != "" {
		req.Header.Set("api-key", p.APIKey)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("supertokens: delete user %s: %w", userID, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	var out struct{ Status string }
	if resp.StatusCode != http.StatusOK || json.Unmarshal(msg, &out) != nil || out.Status != "OK" {
		return fmt.Errorf("supertokens: delete user %s: %s %s", userID, resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}
