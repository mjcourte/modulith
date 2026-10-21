export type OwnerKind = 'user' | 'org' | 'project' | 'modulith'
export type OwnerRef = { kind: OwnerKind; id: string }