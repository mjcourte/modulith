export type OwnerKind = 'user' | 'org' | 'project' | 'gyoza'
export type OwnerRef = { kind: OwnerKind; id: string }