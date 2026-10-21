import SuperTokens from 'supertokens-auth-react'
import Session from 'supertokens-auth-react/recipe/session'
import ThirdParty, { Google } from 'supertokens-auth-react/recipe/thirdparty'

// Must match the gateway's SuperTokens AppInfo (services/gateway/internal/auth).
// The API and the website share one origin: in dev, Vite proxies /auth and
// /api to the gateway (vite.config.ts), so cookies and the no-CORS gateway work.
SuperTokens.init({
    appInfo: {
        appName: 'Modulith',
        apiDomain: window.location.origin,
        websiteDomain: window.location.origin,
        apiBasePath: '/auth',
        websiteBasePath: '/login',
    },
    recipeList: [
        ThirdParty.init({
            signInAndUpFeature: { providers: [Google.init()] },
        }),
        Session.init(),
    ],
})
