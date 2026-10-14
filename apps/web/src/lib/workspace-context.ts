import type { Org, User } from '@/api/me'
import { createContext, useContext } from 'react'

export type WorkspaceKind = 'user' | 'org'

export type Owner = {
    kind: WorkspaceKind
    id: string
}

export type Workspace = {
    owner: Owner
    user: User
    kind: WorkspaceKind
    setKind: (k: WorkspaceKind) => void
    org: Org | null
}

export const WorkspaceCtx = createContext<Workspace | null>(null)

export function useWorkspace() {
    const ws = useContext(WorkspaceCtx)
    if (!ws) throw new Error('useWorkspace outside WorkspaceProvider')
    return ws
}