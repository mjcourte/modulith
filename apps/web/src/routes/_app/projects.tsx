import { useProjects } from '@/api/projects'
import { Button } from '@/components/ui/button'
import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/_app/projects')({ component: ProjectsPage })

function ProjectsPage() {
    const { data, isPending, error, hasNextPage, fetchNextPage, isFetchingNextPage } = useProjects()

    if (isPending) return <p className="p-8">Loading...</p>
    if (error) return <p className="p-8">Couldn't load your projects: {error.message}</p>

    // every page fetched so far, as one list; clones still copying aren't openable yet
    const projects = data.pages.flatMap(page => page.projects).filter(p => p.state === 'ready')

    return (
        <div className='p-8 space-y-4'>
            {projects.length === 0 ? (
                <p className="text-muted-foreground">No projects yet.</p>
            ) : (
                <ul className="space-y-1">
                    {projects.map(p => <li key={p.id}>{p.name}</li>)}
                </ul>
            )}
            {hasNextPage && (
                <Button variant="outline" onClick={() => fetchNextPage()} disabled={isFetchingNextPage}>
                    {isFetchingNextPage ? 'Loading...' : 'Load more'}
                </Button>
            )}
        </div>
    )
}
