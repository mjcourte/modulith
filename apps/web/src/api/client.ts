export class ApiError extends Error {
    readonly status: number
    readonly code: string

    constructor(status: number, code: string, message: string) {
        super(message)
        this.name = 'ApiError'
        this.status = status
        this.code = code
    }
}

type ApiInit = Omit<RequestInit, 'body'> & { json?: unknown }

export async function api<T>(path: string, { json, ...init }: ApiInit = {}): Promise<T> {
    const headers = new Headers(init.headers)
    if (json !== undefined) headers.set('ontent-Type', 'application/json')

    const res = await fetch(path, {
        ...init,
        headers,
        body: json === undefined ? undefined : JSON.stringify(json),
    })

    if (!res.ok) {
        const body = await res.json().catch(() => null)
        const err = body?.error
        throw new ApiError(res.status, err?.code ?? 'unknown', err?.message ?? res.statusText)
    }
    if (res.status === 204) return undefined as T
    return (await res.json()) as T
}
