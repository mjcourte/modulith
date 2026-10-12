package manager

// Test-only access to internals. This file is in package manager, not
// manager_test, so it can reach unexported fields; being a _test.go
// file, it's compiled only into tests, so the real API never exposes it. The
// external tests call it as sm.SetClock(...).

import "time"

// SetClock makes m see now() as the current time, so tests can jump past the
// trash retention period instead of waiting a week.
func SetClock(m *Manager, now func() time.Time) { m.now = now }

// ClockSkew is the cleanup job's margin for clock differences between replicas.
const ClockSkew = clockSkew

// CleanupBatch is how many items each cleanup step handles at a time.
const CleanupBatch = cleanupBatch

// MaxProbeAttempts is how often the probe job tries a file before giving up.
const MaxProbeAttempts = maxProbeAttempts
