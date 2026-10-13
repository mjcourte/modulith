// Package manager is the documents service's logic: Manager implements
// Service (service.go), the API other components call.
//
//   - manager.go: Manager, its Config and dependencies; loading a project's
//     document (from its latest snapshot, or a new one stored as version 1:
//     README "LoadOrInit") and saving it, through internal/live.
//   - manager_documents.go: GetDocument, QueryDocument, InitDocument,
//     UpdateDocument.
//   - authz.go: Authorizer (iam implements it) and ErrForbidden.
//   - uses.go: AssetUses (storage implements it) and keeping storage's usage
//     rows in step with the asset refs each edit adds or removes.
//
// Manager depends on interfaces only (engine.Engine, snapshots.Store,
// Authorizer, AssetUses), so tests wire in-memory fakes of all of them.
package manager
