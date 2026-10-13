import './auth'; // this import must remain first

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRouter, RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { SuperTokensWrapper } from 'supertokens-auth-react'
import './index.css'
import { routeTree } from './routeTree.gen'

const queryClient = new QueryClient()

const router = createRouter({ routeTree })

// Registers the router's type with the library, so <Link to>, params and
// search are type-checked against the generated route tree.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <SuperTokensWrapper>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </SuperTokensWrapper>
  </StrictMode>,
)
