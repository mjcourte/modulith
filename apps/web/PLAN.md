# Plan notes: web app (`apps/web`)

Not started. UI details the backend plans rely on (2026-10-11). Related:
[gateway/PLAN.md](../../services/gateway/PLAN.md),
[iam/PLAN.md](../../services/iam/PLAN.md),
[storagemanager/PLAN.md](../../services/storagemanager/PLAN.md).

## Sign-in and orgs

- Google sign-in via SuperTokens' React SDK (`supertokens-auth-react`, ThirdParty
  recipe), cookie sessions with the gateway.
- After sign-in, show pending org invites (returned by iam `SignIn`) to accept
  or decline. Admins invite by email; no email is sent in the MVP, so the UI says "tell
  them to sign in with this address".

## Projects

- Create project: name, and an owner choice: "me" or "my org" (the org option only for
  members of one). Choosing the org shows an org-lock switch ("only people in this org"),
  on by default; any member creating the project may turn it off. Changing it later is
  for org admins (see Sharing).
- Gyoza templates listed separately; read-only, with a "clone" action.
- Clone any project you can read: choose the target (your library or your org). A
  locked project offers only its org as the target. A clone
  appears once it's ready (it's hidden while copying).

## Sharing

- Editors (owner, org members, editor shares) see the share dialog: add people as viewer
  or editor, change or remove shares. Viewers don't. Everyone can leave a project
  shared with them.
- Org projects: admins get an "only people in this org" (org-lock) switch. While it's
  on, outside people can't be added and existing outside shares show as suspended
  (they come back if the lock is turned off).

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
