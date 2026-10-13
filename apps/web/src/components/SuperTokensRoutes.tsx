import { Navigate } from '@tanstack/react-router'
import { ThirdPartyPreBuiltUI } from 'supertokens-auth-react/recipe/thirdparty/prebuiltui'
import { canHandleRoute, getRoutingComponent } from 'supertokens-auth-react/ui'

const preBuiltUIs = [ThirdPartyPreBuiltUI]

// Renders SuperTokens' prebuilt page for the current URL (under websiteBasePath,
// see src/auth.ts): the sign-in screen, or the Google callback that exchanges
// the code with the gateway.
export function SuperTokensRoutes() {
    if (!canHandleRoute(preBuiltUIs)) {
        return <Navigate to="/login" replace />
    }
    return getRoutingComponent(preBuiltUIs)
}
