package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/supertokens/supertokens-golang/recipe/thirdparty/tpmodels"
	"github.com/supertokens/supertokens-golang/supertokens"

	"gyoza/services/iam"
)

// fakeUsers is iam's SignIn: it records identities and fails on demand.
type fakeUsers struct {
	got  []iam.Identity
	fail error
}

func (u *fakeUsers) SignIn(ctx context.Context, id iam.Identity) (iam.SignInResult, error) {
	if u.fail != nil {
		return iam.SignInResult{}, u.fail
	}
	u.got = append(u.got, id)
	return iam.SignInResult{}, nil
}

// hooked returns SignInUp as the SDK would call it: the hook wrapping an
// original implementation that answers with orig.
func hooked(users Users, orig func() (tpmodels.SignInUpResponse, error)) func(tpmodels.TypeRawUserInfoFromProvider) (tpmodels.SignInUpResponse, error) {
	origSignInUp := func(string, string, string, tpmodels.TypeOAuthTokens, tpmodels.TypeRawUserInfoFromProvider, string, supertokens.UserContext) (tpmodels.SignInUpResponse, error) {
		return orig()
	}
	ri := signInHook(users)(tpmodels.RecipeInterface{SignInUp: &origSignInUp})
	return func(raw tpmodels.TypeRawUserInfoFromProvider) (tpmodels.SignInUpResponse, error) {
		return (*ri.SignInUp)("google", "g-123", "ann@example.com", nil, raw, "public", nil)
	}
}

func signedUp(userID string) (tpmodels.SignInUpResponse, error) {
	var resp tpmodels.SignInUpResponse
	resp.OK = &struct {
		CreatedNewUser          bool
		User                    tpmodels.User
		OAuthTokens             tpmodels.TypeOAuthTokens
		RawUserInfoFromProvider tpmodels.TypeRawUserInfoFromProvider
	}{CreatedNewUser: true, User: tpmodels.User{ID: userID, Email: "ann@example.com"}}
	return resp, nil
}

func TestSignInRecordsTheUserInIAM(t *testing.T) {
	users := &fakeUsers{}
	signIn := hooked(users, func() (tpmodels.SignInUpResponse, error) { return signedUp("st-ann") })
	raw := tpmodels.TypeRawUserInfoFromProvider{FromIdTokenPayload: map[string]any{
		"email_verified": true, "name": "Ann", "picture": "https://example.com/ann.png",
	}}
	resp, err := signIn(raw)
	if err != nil || resp.OK == nil || resp.OK.User.ID != "st-ann" {
		t.Fatalf("SignInUp = %+v, %v; want the original response", resp, err)
	}
	want := iam.Identity{UserID: "st-ann", Email: "ann@example.com", EmailVerified: true, Name: "Ann", ProviderAvatarURL: "https://example.com/ann.png"}
	if len(users.got) != 1 || users.got[0] != want {
		t.Errorf("iam got %+v, want [%+v]", users.got, want)
	}
}

// No session without an iam user: a failed iam sign-in fails the sign-in.
func TestSignInFailsWhenIAMFails(t *testing.T) {
	outage := errors.New("iam unavailable")
	signIn := hooked(&fakeUsers{fail: outage}, func() (tpmodels.SignInUpResponse, error) { return signedUp("st-ann") })
	resp, err := signIn(tpmodels.TypeRawUserInfoFromProvider{})
	if !errors.Is(err, outage) || resp.OK != nil {
		t.Errorf("SignInUp = %+v, %v; want no OK and the iam error", resp, err)
	}
}

// iam hears only about sign-ins SuperTokens accepted.
func TestSignInSkipsIAMWhenSuperTokensRefuses(t *testing.T) {
	users := &fakeUsers{}
	// A response without OK: no user was signed in.
	signIn := hooked(users, func() (tpmodels.SignInUpResponse, error) { return tpmodels.SignInUpResponse{}, nil })
	if resp, err := signIn(tpmodels.TypeRawUserInfoFromProvider{}); err != nil || resp.OK != nil {
		t.Errorf("SignInUp = %+v, %v; want the empty response passed through", resp, err)
	}
	coreDown := errors.New("core unreachable")
	signIn = hooked(users, func() (tpmodels.SignInUpResponse, error) { return tpmodels.SignInUpResponse{}, coreDown })
	if _, err := signIn(tpmodels.TypeRawUserInfoFromProvider{}); !errors.Is(err, coreDown) {
		t.Errorf("err = %v, want the core's", err)
	}
	if len(users.got) != 0 {
		t.Errorf("iam signed in %+v after a refused sign-in", users.got)
	}
}

func TestIdentityFromClaims(t *testing.T) {
	tests := []struct {
		name string
		raw  tpmodels.TypeRawUserInfoFromProvider
		want iam.Identity
	}{
		{"no claims", tpmodels.TypeRawUserInfoFromProvider{}, iam.Identity{UserID: "u", Email: "e"}},
		{"user info API only", tpmodels.TypeRawUserInfoFromProvider{
			FromUserInfoAPI: map[string]any{"email_verified": "true", "name": "From API"},
		}, iam.Identity{UserID: "u", Email: "e", EmailVerified: true, Name: "From API"}},
		{"ID token wins", tpmodels.TypeRawUserInfoFromProvider{
			FromIdTokenPayload: map[string]any{"email_verified": false, "name": "From token"},
			FromUserInfoAPI:    map[string]any{"email_verified": true, "name": "From API"},
		}, iam.Identity{UserID: "u", Email: "e", Name: "From token"}},
		{"unverified string", tpmodels.TypeRawUserInfoFromProvider{
			FromIdTokenPayload: map[string]any{"email_verified": "false"},
		}, iam.Identity{UserID: "u", Email: "e"}},
	}
	for _, tt := range tests {
		if got := identityFrom("u", "e", tt.raw); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
