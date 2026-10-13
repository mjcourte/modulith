package snapshots_test

import (
	"testing"

	"gyoza/services/storage/internal/snapshots"
)

func TestMemory(t *testing.T) {
	runStoreTests(t, func(*testing.T) snapshots.Store { return snapshots.NewMemory() }, true)
}
