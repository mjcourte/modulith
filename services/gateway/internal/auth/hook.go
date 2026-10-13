package auth

import (
	"context"
	"fmt"

	"github.com/supertokens/supertokens-golang/recipe/thirdparty/tpmodels"
	"github.com/supertokens/supertokens-golang/supertokens"

	"gyoza/services/iam"
)

// Users is what sign-in needs from iam: recording the user on every sign-in.
// iam.Service satisfies it, in-process or over gRPC.
type Users interface {
	SignIn(ctx context.Context, id iam.Identity) (iam.SignInResult, error)
}

// signInHook wraps ThirdParty's SignInUp so every successful sign-in also
// signs the user in to iam, which creates or updates their row. It runs
// before the session is created, so if iam fails, the sign-in fails: there
// is never a session without an iam user. The SuperTokens account may then
// exist without one; the next sign-in creates it (SignIn is an upsert).
func signInHook(users Users) func(tpmodels.RecipeInterface) tpmodels.RecipeInterface {
	return func(orig tpmodels.RecipeInterface) tpmodels.RecipeInterface {
		origSignInUp := *orig.SignInUp
		signInUp := func(thirdPartyID, thirdPartyUserID, email string, tokens tpmodels.TypeOAuthTokens,
			raw tpmodels.TypeRawUserInfoFromProvider, tenantID string, uc supertokens.UserContext) (tpmodels.SignInUpResponse, error) {
			resp, err := origSignInUp(thirdPartyID, thirdPartyUserID, email, tokens, raw, tenantID, uc)
			if err != nil || resp.OK == nil {
				return resp, err
			}
			ctx := context.Background()
			if r := supertokens.GetRequestFromUserContext(uc); r != nil {
				ctx = r.Context()
			}
			if _, err := users.SignIn(ctx, identityFrom(resp.OK.User.ID, email, raw)); err != nil {
				return tpmodels.SignInUpResponse{}, fmt.Errorf("gateway: iam sign-in of %s: %w", resp.OK.User.ID, err)
			}
			return resp, nil
		}
		orig.SignInUp = &signInUp
		return orig
	}
}

// identityFrom builds iam's Identity from what the provider returned: the
// SuperTokens user ID (stable across sign-ins, iam's user ID), the email,
// and Google's OpenID claims (email_verified, name, picture), preferring the
// ID token over the user info API.
func identityFrom(userID, email string, raw tpmodels.TypeRawUserInfoFromProvider) iam.Identity {
	claim := func(name string) any {
		if v, ok := raw.FromIdTokenPayload[name]; ok {
			return v
		}
		return raw.FromUserInfoAPI[name]
	}
	str := func(name string) string {
		s, _ := claim(name).(string)
		return s
	}
	// Google sends a JSON boolean; some providers send the string "true".
	verified := false
	switch v := claim("email_verified").(type) {
	case bool:
		verified = v
	case string:
		verified = v == "true"
	}
	return iam.Identity{
		UserID:            userID,
		Email:             email,
		EmailVerified:     verified,
		Name:              str("name"),
		ProviderAvatarURL: str("picture"),
	}
}
