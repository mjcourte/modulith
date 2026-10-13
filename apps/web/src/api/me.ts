import { useQuery } from '@tanstack/react-query';
import { api } from './client';

export type User = { id: string; email: string; emailVerified: boolean; name: string; avatarUrl: string; createdAt: string }
export type Org = { id: string; name: string; createdAt: string }
export type Role = "admin" | "member"
export type Member = { orgId: string; userId: string; role: Role; joinedAt: string }
export type Invite = { id: string; orgId: string; email: string; role: Role; invitedBy: string; createdAt: string; expiresAt: string }
export type Me = { user: User; org: Org | null; membership: Member | null; invites: Invite[] }

export function useMe() {
    return useQuery({ queryKey: ['me'], queryFn: () => api<Me>('/api/me') })
}