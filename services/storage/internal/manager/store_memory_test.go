package manager_test

import (
	"testing"

	sm "gyoza/services/storage/internal/manager"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) sm.Store {
		return sm.NewMemoryStore()
	})
}
