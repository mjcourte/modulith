package directory_test

import (
	"github.com/mjcourte/modulith/services/iam/internal/directory"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) directory.Store { return directory.NewMemoryStore() })
}
