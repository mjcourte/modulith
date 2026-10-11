# Storage Ledger

- A PostGreSQL ledger for all uploads to S3
- Relates to user DB as needed eg. GDPR deletion

## Model

- every object has exactly one owner: a project, a user, an org, or Gyoza (shared/common files)
- an object's lifetime follows its owner: deleting an owner deletes everything it owns (`List{Owner: x}`)
- `CreatedBy` is the user who uploaded/produced it; can differ from the owner (e.g. org assets)
- `ProjectID` is optional and means "associated with this project", whoever owns it, so `List{ProjectID: p}` lists everything for a project. A project-owned object always has `ProjectID` set to its owner (enforced on create)
- a project has exactly one document (or none until the document manager initializes it), per the engine's `App`, so there is no separate document scope
- `Kind`: snapshot, asset, export (initial set). It says what **role** a stored object plays, for every object in S3. Not to be confused with storagemanager's DAM `Category` (image, video, font, ...), which says what **kind of media** an asset is and exists only for DAM assets
- `ContentType`: the object's MIME type. Document snapshots should use a dedicated vendor type (e.g. `application/vnd.gyoza.app+flatbuffers`) so any service can tell a snapshot's format from the ledger alone
- user/org/project IDs are opaque strings owned by user-manager, not foreign keys

## Caller-chosen IDs and purging

- `Create` assigns an ID, or takes one the caller chose. With a chosen ID it is idempotent: the same file again (bucket, key, size, type, kind, project) returns the existing object; a different file is `ErrAlreadyExists`. Owner and creator are not compared, since transfers and user removal change them. storagemanager chooses the ID when an upload begins, so retries can never record a file twice
- chosen IDs: at most 128 bytes, printable ASCII, no spaces
- one ledger object per S3 key is an invariant the writers keep (not enforced by the schema: the tests reuse keys): purging an object deletes its file
- `ListDeleted(before, limit)` lists deleted objects longest-deleted first; `Purge(id)` removes a deleted object's row for good (a live one is `ErrNotDeleted`; a missing one is fine). Only storagemanager's cleanup job calls them, after deleting the file

## Queries

- one `List` with exactly one scope (project or owner) plus optional filters (`CreatedBy`, `Kind`, `IncludeDeleted`)
- only add a filter when something needs it; each one must be supported and indexed by every implementation
- keyset pagination ordered by (`CreatedAt`, `ID`) with opaque page tokens

- performance findings and known things to optimize: [OPTIMIZATIONS.md](OPTIMIZATIONS.md)

## API

- in-process: the `Service` interface (`Memory`, `Postgres`, `GRPCClient`)
- over the network: `LedgerService` in `contracts/proto/gyoza/storageledger/v1/ledger.proto`. `GRPCServer` exposes any `Service`; `GRPCClient` is a `Service` that calls one remotely, returning the same types and sentinel errors (`errors.Is(err, ErrNotFound)` works across the wire)
- error mapping: `ErrNotFound` -> NOT_FOUND, `ErrInvalidQuery`/`ErrInvalidObject` -> INVALID_ARGUMENT, `ErrAlreadyExists` -> ALREADY_EXISTS, `ErrNotDeleted` -> FAILED_PRECONDITION, anything else -> INTERNAL with the details logged server-side only
- the monolith uses `Postgres` directly; `cmd/storage-ledger` serves it on `GYOZA_GRPC_ADDR`

## Project deletion

- delete everything owned by the project (`MarkDeletedByOwner(project)`); objects owned by others but associated with the project survive
- storagemanager's `DeleteOwned` does this together with the DAM side
- planned: user-manager publishes `project.deleted` on JetStream; a cleanup consumer deletes from S3 (via storage-manager) and marks deleted here, so a crash mid-cascade resumes on redelivery
- open: dangling references when a deleted owner's assets are still used in other documents (placeholder in the engine? org owns uploads into org projects?)

## User deletion (GDPR)

Orchestrated by user-manager through storagemanager's `RemoveUser`; the ledger provides the bulk operations.

- user leaves **with an org** (a user has at most one): everything they own moves to the org (`TransferOwner(user, org)`)
- user leaves **without an org**: everything they own is deleted (`MarkDeletedByOwner(user)`)
- either way, the user is cleared as creator everywhere (`ClearCreatedBy`, which also covers soft-deleted rows)
- last user leaves an org: the org's objects are deleted (`MarkDeletedByOwner(org)`)
- projects never transfer: `TransferOwner` refuses project owners on either side
- resolved: soft-deleted rows are hard-purged (`Purge`) by storagemanager's cleanup job once its retention period (7 days) is over, after their files are deleted from S3
- resolved: S3 keys never embed user or org IDs; see the key layout in services/storagemanager/README.md
