# Admin app

A development-only web UI for looking at and poking the backend: users, orgs, members,
invites, projects, shares, assets (versions, media info, uses), folders, ledger objects
and uploads, linked to each other. A project's page shows its live document and its
stored snapshots, with a button to revert to any of them (administrative un-sticking:
one fresh edit, see services/documents/PLAN.md "Reverting").

```sh
go run ./cmd/modulith                    # in one terminal (needs compose's RustFS)
MODULITH_ADMIN=1 go run ./cmd/admin      # in another; open http://localhost:8081
```

## How it works (decided 2026-10-12)

- **No gateway.** The gateway's job is turning a SuperTokens session into `actor_id`
  and never letting the caller choose it. The admin needs the opposite: pick any user
  in the header ("acting as") and every change is made as them, so authz is exercised
  for real (acting as someone else shows the service's `forbidden` error).
- **Reads are SQL** over `iam.*` and `storage.*` (`queries.go`). A dev tool is exempt
  from the one-schema-per-service rule; it sees everything, trashed and deleting rows
  included. Lists stop at 500 rows.
- **Writes go through the services** (`iam.Service`, `storage.Service`, `documents.Service` gRPC clients),
  never SQL, so the rules, the ledger and S3 stay consistent. Calls without an actor
  (`CreateTemplate`, `DeleteTemplate`, `DeleteOwned`) are plain buttons.
- New users are created with `SignIn` and a made-up identity, as the gateway's sign-in
  hook will. Uploads run the two-phase flow server-side (BeginUpload, PUT to the
  presigned URL, CompleteUpload), so the bucket needs no CORS rules; downloads redirect
  to the presigned URL.
- Go `html/template` + vendored htmx 2.0.4 (`static/`, only for the live search boxes),
  embedded in the binary. No Node build.
- Refuses to start without `MODULITH_ADMIN=1`, listens on `localhost:8081`
  (`MODULITH_ADMIN_ADDR`), and calls iam and storage on `localhost:9090`
  (`MODULITH_ADMIN_IAM_ADDR`, `MODULITH_ADMIN_STORAGE_ADDR`, `MODULITH_ADMIN_DOCUMENTS_ADDR`;
  point them at 9093/9091/9094 for the distributed binaries). Never deploy it.
- No authentication, so other sites are kept out instead (`guard`): requests must be
  addressed to `localhost` or a loopback IP (against DNS rebinding), and changes must be
  same-origin (`http.CrossOriginProtection`, against forms posted from other pages).

## Not yet

- Editing asset metadata, folder create/move, invites accept flow per user beyond the
  buttons, paging past 500 rows, a relation graph view.
