# Storage Catalogue

- A PostGreSQL ledger for all uploads to S3
- Relates to user DB as needed eg. GDPR deletion

## Model

- every object has exactly one owner: a project, a user, an org, or Gyoza (shared/common files)
- an object's lifetime follows its owner: deleting an owner deletes everything it owns (`List{Owner: x}`)
- `CreatedBy` is the user who uploaded/produced it; can differ from the owner (e.g. org assets)
- `ProjectID` is optional and means "associated with this project", whoever owns it, so `List{ProjectID: p}` lists everything for a project. A project-owned object always has `ProjectID` set to its owner (enforced on create)
- a project has exactly one document (or none until the document server initializes it), per the engine's `App`, so there is no separate document scope
- `Kind`: snapshot, asset, export (initial set)
- user/org/project IDs are opaque strings owned by user-manager, not foreign keys

## Queries

- one `List` with exactly one scope (project or owner) plus optional filters (`CreatedBy`, `Kind`, `IncludeDeleted`)
- only add a filter when something needs it; each one must be supported and indexed by every implementation
- keyset pagination ordered by (`CreatedAt`, `ID`) with opaque page tokens

- performance findings and known things to optimize: [OPTIMIZATIONS.md](OPTIMIZATIONS.md)

## Project deletion

- delete everything owned by the project (`List{Owner: project}`); objects owned by others but associated with the project survive
- planned: user-manager publishes `project.deleted` on JetStream; a cleanup consumer deletes from S3 (via storage-manager) and marks deleted here, so a crash mid-cascade resumes on redelivery
- open: dangling references when a deleted owner's assets are still used in other documents (placeholder in the engine? org owns uploads into org projects?)

## User deletion (GDPR)

Orchestrated by user-manager; the catalogue only provides the queries.

- user leaves: delete everything the user owns, whoever created it (`List{Owner: user}`)
- objects owned by someone else (e.g. org assets) stay, and the user is cleared as their creator (`ClearCreatedBy`, which also covers soft-deleted rows)
- orgs are optional; an org-less user only has user-owned objects
- last user leaves an org: clean up the org's objects too
- open: soft-deleted rows still hold metadata; when are they hard-purged?
- open: S3 keys must not embed user IDs for objects that outlive the user (e.g. org assets)
