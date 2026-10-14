import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from './client';
import type { OwnerKind } from "./owner";

export type ProjectState = 'ready' | 'cloning'
export type ProjectOwner = { kind: Exclude<OwnerKind, 'project'>; id: string }

export type Project = {
    id: string
    name: string
    state: ProjectState
    createdBy: string
    owner: ProjectOwner
    clonedFrom?: string
    orgLocked: boolean
    createdAt: string
    updatedAt: string
}

export type ProjectPage = {
    projects: Project[]
    nextPageToken: string
}

export function useProjects() {
    return useInfiniteQuery({
        queryKey: ['projects'],
        queryFn: ({ pageParam }) => api<ProjectPage>(`/api/projects?${new URLSearchParams({ pageToken: pageParam })}`),
        initialPageParam: '',
        getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    })
}

export function useTemplates() {
    return useInfiniteQuery({
        queryKey: ['templates'],
        queryFn: ({ pageParam }) => api<ProjectPage>(`/api/templates?${new URLSearchParams({ pageToken: pageParam })}`),
        initialPageParam: '',
        getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    })
}