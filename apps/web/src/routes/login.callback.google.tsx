import { SuperTokensRoutes } from '@/components/SuperTokensRoutes'
import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/login/callback/google')({
    component: SuperTokensRoutes,
})
