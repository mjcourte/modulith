package snapshots_test

import (
	"testing"

	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/snapshots/snapshotstest"
)

func TestMemory(t *testing.T) {
	snapshotstest.RunSuite(t, func(*testing.T) snapshots.Store { return snapshots.NewMemory() })
}
