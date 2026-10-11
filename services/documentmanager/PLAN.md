# Plan notes: document-manager (`services/documentmanager`)

Not started. These are the commitments other services' plans make on its behalf
(2026-10-11). Related: [storagemanager/PLAN.md](../storagemanager/PLAN.md) (asset refs,
usage, forking), [usermanager/PLAN.md](../usermanager/PLAN.md) (cloning),
[gateway/PLAN.md](../gateway/PLAN.md) (auth).

## Asset references

- Documents point at assets only with `AssetRef{AssetID, Version}` (storagemanager's
  type and proto message), always pinned to a version. "Use the latest" is resolved
  when inserting. Showing "update available" and moving a ref to a newer version is a
  deliberate document edit.
- Assets are read through storagemanager's `ResolveRef(actor, projectID, ref)`, which
  also works while the asset is in the trash (until purged) and returns `DeletedAt` so
  the UI can mark it.

## Usage rows

- Keep storagemanager's usage table accurate: record `(project, asset, version)` when a
  ref is added to a document, forget it when the last use is removed (both idempotent).
  Access to used assets and what a clone copies both depend on these rows.

## Cloning and forking

- Clone (usermanager `CloneProject` -> storagemanager `CopyProjectFiles` -> here): copy
  the document into the clone project and rewrite every ref to its copy,
  `{UUIDv5(clone project, asset, version), 1}`. Computable locally: no mapping table.
  Must be idempotent, since the clone job can resume.
- Fork (storagemanager `ForkAsset`): rewrite the forked ref in that one document to the
  returned ref.

## Open

- Whether browsers connect to replicas directly; if so, replicas verify SuperTokens
  access tokens themselves (see gateway/PLAN.md).
