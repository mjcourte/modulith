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
- the Manager is the only code that builds keys (one function); callers never pass a key in. Other services that write to S3 (document manager snapshots, exports) must use the same layout

## URL lifetimes

- `Blobs` takes an explicit TTL (mechanism); how long URLs last is Manager policy
- defaults: upload URLs 15 min, download URLs 5 min, overridable with `GYOZA_UPLOAD_URL_TTL` / `GYOZA_DOWNLOAD_URL_TTL`
- S3 checks expiry when a request starts, so a long upload that starts in time still completes

## IDs

- presign tokens (memory fake): `crypto/rand.Text()`, since they only need to be unguessable
- table IDs (assets, and later folders and upload sessions): UUIDv7 via `github.com/google/uuid`. They start with a timestamp, so ID order is creation order: paging orders by ID alone, and Postgres inserts land at the end of the index (cheaper than the ledger's random IDs, see storageledger/OPTIMIZATIONS.md)
- ordering is guaranteed within one process (the library adds a sequence number inside each millisecond). Across replicas, IDs made in the same millisecond may interleave; paging stays correct, "oldest first" is just approximate to the millisecond

## DAM

Assets are named, tagged files in a user's, org's or project's library.
Types and the `Store` interface are in `assets.go`; the rules every Store
enforces are in `assets_rules.go`; limits are constants at the top of `assets.go`.

- **asset**: owner (same `Owner` type and rules as the ledger, via `Owner.Validate`), optional `ProjectID` (association, as in the ledger), folder, name, description, tags, content type, category, current version, timestamps
- **versions**: each upload of a file is a version pointing at a ledger object. Append-only: a new upload becomes version N+1 and current; "revert" makes an older version current again without copying anything; reverting to the current version changes nothing (not even `UpdatedAt`), so retries are harmless. Versions are removed only together with their asset
- **tags**: free-form, normalised (trimmed, lowercased, deduplicated, sorted), so "Logo", " logo" and "LOGO" are one tag. A set per asset; `Tags` in an update replaces the whole set
- **categories**: `image`, `video`, `audio`, `font`, `pdf`, `json`, `text`, `other`, derived from the MIME type with `CategoryOf`, so an asset's kind is known without reading bytes or asking the ledger. Fixed for the asset's life: a new version must have the same category (png -> jpeg yes, image -> video no)
- **document snapshots are not assets**: nobody names, tags or re-uploads them, and their history is the document's own log. They live only in the ledger (`Kind=snapshot`). A `document` category comes only if library templates/components are designed later
- **listing**: exactly one scope (project or owner) plus optional tag and category filters; oldest first, exact thanks to UUIDv7. Folder listing arrives with folders; name search later
- **deletion**: soft. A deleted asset stays readable (get, versions, list with `IncludeDeleted`) but can't be changed: update, add version and revert return `ErrAssetNotFound`
- **partial updates**: `AssetUpdate` fields are pointers; `nil` leaves a field alone, a pointer to `""` or an empty slice clears it

## Authorization

- the gateway authenticates (who is calling); services authorize (what they may do)
- Manager methods (step 4) take the acting user's ID and ask an `Authorizer` interface declared here: may this user read/write files for this owner or project?
- usermanager implements it: org existence, membership and project sharing (allowed users) all live there. storagemanager never reads usermanager's tables. Until usermanager exists, an allow-all stand-in is used
- the `Store` does no authorization: it only checks data shape and trusts the Manager

## Code map

| File | What |
|---|---|
| `blobs.go` | `Blobs` interface (S3 abstraction: presign PUT/GET, stat, delete) |
| `blobs_memory.go` | `MemoryBlobs` fake, plus `PutSigned`/`GetSigned` helpers that play the browser in tests |
| `assets.go` | errors, limits, DAM types, `CategoryOf`, `Store` interface |
| `assets_rules.go` | validation, normalisation and paging helpers shared by every Store |
| `assets_memory.go` | `MemoryStore` |
| `blobs_test.go` | shared `runBlobsTests` suite (memory now, S3 later) |
| `store_test.go` | shared `runStoreTests` suite (memory now, Postgres later) |
| `assets_rules_test.go` | internal unit tests of the unexported helpers |

Both shared suites were mutation-checked: realistic bugs reintroduced into the fakes are each caught by a named test.

## Todo: media metadata

Know an image/video/audio file's properties without fetching and inspecting its bytes, by extracting them once when it's stored.

- what: images: width, height, (color space, has alpha); video: width, height, duration, frame rate, bitrate, codecs, audio tracks; audio: duration, sample rate, channels, bitrate, codec. Maybe PDF page count, font family name later
- belongs to a **version**, not the asset: a replacement file can have a different resolution
- storage: typed columns for what we filter or sort by (width, height, duration), plus a JSON column for the rest, so new fields don't need migrations
- images: Go's `image.DecodeConfig` reads only the header (the first few KB, via `Blobs.ReadPrefix`), so it can run inline in `CompleteUpload`
- video/audio: `ffprobe` (media-processor) reads the container metadata over a presigned URL using HTTP range requests, without downloading the whole file. Too slow to run inline, so a job after `CompleteUpload` fills the metadata in; until then a version's metadata is "pending"
- open: whether exports (also media) get the same metadata, recorded by whoever writes them
