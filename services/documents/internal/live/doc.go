// Package live keeps the documents that are open in memory (README
// "LoadOrInit" and "DebouncedExecutor").
//
//   - Registry.Acquire loads a project once however many callers ask, and
//     holds it while in use (live.go).
//   - Doc.Update changes it and arms a debounced save: after Debounce of
//     quiet, at most MaxDelay after the first unsaved change; failures retry
//     with backoff (document.go).
//   - An idle document is saved and evicted after TTL; one that can't be
//     saved stays in memory.
//   - Registry.Close saves everything still unsaved.
//
// Loading and saving are injected (Loader, Saver), so the package knows
// nothing about storage or NATS. Timers use a clock interface (clock.go);
// tests replace it with a fake (export_test.go).
package live
