package iam

// Test-only access to internals: in package iam, so it can reach unexported
// fields, but compiled only into tests (see storage's internal/manager/export_test.go).

import "time"

// SetClock makes m see now() as the current time.
func SetClock(m *Manager, now func() time.Time) { m.now = now }
