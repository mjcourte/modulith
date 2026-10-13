// Package snapshots is storage's record of document snapshots (table
// storage.snapshots): data only, like the ledger. The Manager does the S3
// and ledger side (manager_snapshots.go).
//
//   - snapshots.go: Snapshot, the Store interface, errors and the rules every
//     Store applies (stale check, validation).
//   - memory.go, postgres.go: the two Stores; service_test.go is the shared
//     suite both run.
//
// A snapshot is pending from Begin until Complete gives it the project's
// next version. Complete never lets a project's latest go back to an earlier
// stream sequence (ErrStale), atomically per project.
package snapshots
