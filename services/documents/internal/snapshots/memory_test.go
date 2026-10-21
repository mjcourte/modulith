package snapshots_test

import (
	"testing"

	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
	"github.com/mjcourte/modulith/services/documents/internal/snapshots/snapshotstest"
)

func TestMemory(t *testing.T) {
	snapshotstest.RunSuite(t, func(*testing.T) snapshots.Store { return snapshots.NewMemory() })
}
