# Plan notes: web app (`apps/web`)

Not started. UI details the backend plans rely on (2026-10-11). Related:
[gateway/PLAN.md](../../services/gateway/PLAN.md),
[usermanager/PLAN.md](../../services/usermanager/PLAN.md),
[storagemanager/PLAN.md](../../services/storagemanager/PLAN.md).

## Sign-in and orgs

- Google sign-in via SuperTokens' React SDK (`supertokens-auth-react`, ThirdParty
  recipe), cookie sessions with the gateway.
- After sign-in, show pending org invites (returned by usermanager `SignIn`) to accept
  or decline. Admins invite by email; no email is sent in the MVP, so the UI says "tell
  them to sign in with this address".

## Projects

- Gyoza templates listed separately; read-only, with a "clone" action.
- Clone any project you can read: choose the target (your library or your org). A clone
  appears once it's ready (it's hidden while copying).

## Assets in documents

- Refs are pinned versions: show "update available" when the asset has a newer version,
  and let the user move the ref.
- Trashed but used: an asset in the trash still renders, marked as trashed, with when it
  will disappear (`DeletedAt` + retention); after the purge show it as missing.
- Trashing an asset that projects use: warn with the usage count first.
- Editing a shared asset (one you can read but not write): explain "this is shared:
  editing makes your own copy", then call `ForkAsset` and edit the copy.

## Files

- Deleting a non-empty folder: offer rm -rf with the counts from `FolderNotEmptyError`.
