import { useMe } from '@/api/me'
import { AppSidebar } from '@/components/AppSidebar'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { WorkspaceProvider } from '@/lib/workspace'
import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'
import Session from 'supertokens-auth-react/recipe/session'

export const Route = createFileRoute('/_app')({
    beforeLoad: async () => {
        if (!(await Session.doesSessionExist())) {
            throw redirect({ to: '/login' })
        }
    },
    component: AppLayout
})

function AppLayout() {
    const { data, isPending, error } = useMe()

    if (isPending) return <p className="p-8">Loading...</p>
    if (error) return <p className="p-8">Couldn't load your account: {error.message}</p>

    // const { user, org } = data
    // sidebar later

    return (
        <WorkspaceProvider me={data}>
            <SidebarProvider>
                <AppSidebar />
                <SidebarInset>
                    <SidebarTrigger />
                    <Outlet />
                </SidebarInset>
            </SidebarProvider>
        </WorkspaceProvider >
    )
}