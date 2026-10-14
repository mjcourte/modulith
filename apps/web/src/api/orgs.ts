import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import type { Member, Org } from './me'

export type CreateOrgResult = { org: Org; membership: Member }

// Creates an org with the caller as its admin. Refetches me, which carries the org.
export function useCreateOrg() {
    const queryClient = useQueryClient()
    return useMutation({
        mutationFn: (name: string) => api<CreateOrgResult>('/api/orgs', { method: 'POST', json: { name } }),
        onSuccess: () => queryClient.invalidateQueries({ queryKey: ['me'] }),
    })
}
