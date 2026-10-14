import type { Me } from '@/api/me'
import { useState, type ReactNode } from 'react'
import { WorkspaceCtx, type Owner, type Workspace, type WorkspaceKind } from './workspace-context'

const storageKey = 'gyoza.workspace'

// The last choice in this browser; storage can throw (private windows).
function readStored(): WorkspaceKind {
    try {
        return localStorage.getItem(storageKey) === 'org' ? 'org' : 'user'
    } catch {
        return 'user'
    }
}

export function WorkspaceProvider({ me, children }: { me: Me; children: ReactNode }) {
    const [stored, setStored] = useState<WorkspaceKind>(() => readStored())

    const setKind = (k: WorkspaceKind) => {
        setStored(k)
        try {
            localStorage.setItem(storageKey, k)
        } catch {
            // remembering the choice is a convenience; ignore failures
        }
    }

    // A stored 'org' falls back to the user once they've left the org.
    const owner: Owner = stored === 'org' && me.org
        ? { kind: 'org', id: me.org.id }
        : { kind: 'user', id: me.user.id }

    const value: Workspace = { owner, kind: owner.kind, setKind, org: me.org, user: me.user }
    return <WorkspaceCtx.Provider value={value}>{children}</WorkspaceCtx.Provider>
}
