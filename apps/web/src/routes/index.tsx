import { useMe } from '@/api/me'
import { Button } from '@/components/ui/button'
import { UserAvatar } from '@/components/UserAvatar'
import { createFileRoute, redirect } from '@tanstack/react-router'
import Session from 'supertokens-auth-react/recipe/session'

export const Route = createFileRoute('/')({
    beforeLoad: async () => {
        if (!(await Session.doesSessionExist())) {
            throw redirect({ to: '/login' })
        }
    },
    component: Home,
})

function Home() {
    const { data, isPending, error } = useMe()

    if (isPending) return <p className="p-8">Loading...</p>
    if (error) return <p className="p-8">Couldn't load your account: {error.message}</p>

    const { user, org } = data

    return (
        <div className="p-8 space-y-4">
            <h1 className="text-3xl font-bold">Gyoza</h1>
            <div className="flex items-center gap-3">
                <UserAvatar user={user} size="lg" />
                <div>
                    <p className="font-medium">{user.name}</p>
                    <p className="text-sm text-muted-foreground">{user.email}</p>
                    {org && <p className="text-sm text-muted-foreground">Org: {org.name}</p>}
                </div>
                <Button onClick={async () => {
                    await Session.signOut()
                    window.location.assign('/login')
                }}>Sign out</Button>
            </div>
        </div>
    )
}
