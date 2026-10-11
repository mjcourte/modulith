# Storage Manager

- General S3 putter/getter that should gRPC to storage-ledger
- uploads/user assets/etc
- not concerned with document files

## Decisions

- uploads and downloads use presigned S3 URLs: the browser talks to S3 directly, our servers never carry file bytes
- upload is two-phase: begin (pending session + presigned PUT that signs size and content type), then complete (verify the blob, record it in the ledger)
- HTTP routes live in the gateway; this service exposes a Go `Service` (+ gRPC later), like the ledger
- DAM metadata (names, descriptions, tags, folders, versions) lives in this service's own Postgres schema; each asset version points at a ledger object ID
- user removal: transfer the user's objects to their org (a user has at most one), or delete them if they have none

## S3 key layout

Keys only contain facts that never change and aren't personal data. S3 can't
rename (a "move" is copy + delete), so anything that can change, like the owner
or the DAM folder, stays out of the key; the ledger is the index for those.

```
projects/{projectID}/{kind}/{id}    objects associated with a project
library/{kind}/{id}                 objects with no project (user/org asset libraries)
```

- `kind`: the ledger `Kind` (`asset`, `export`, `snapshot`)
- `id`: a random ID that makes the key unique
- never in a key: user IDs, org IDs, owner kind, folder names, file names
- why this shape: per-project browsing in the S3 console, lifecycle rules by prefix (e.g. expire `projects/*/export/`), bulk delete by project prefix if ever needed, and one prefix per project spreads S3's per-prefix request limits
- the Manager is the only code that builds keys (one function); callers never pass a key in. Other services that write to S3 (document server snapshots, exports) must use the same layout

## URL lifetimes

- `Blobs` takes an explicit TTL (mechanism); how long URLs last is Manager policy
- defaults: upload URLs 15 min, download URLs 5 min, overridable with `GYOZA_UPLOAD_URL_TTL` / `GYOZA_DOWNLOAD_URL_TTL`
- S3 checks expiry when a request starts, so a long upload that starts in time still completes

## IDs

- presign tokens (memory fake): `crypto/rand.Text()`, since they only need to be unguessable
- open: use UUIDv7 for this service's table IDs (time-ordered, cheaper B-tree inserts; see storageledger/OPTIMIZATIONS.md). Decide before step 7 creates the schema
