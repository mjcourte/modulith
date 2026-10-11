package storagemanager_test

import (
	"testing"

	sm "gyoza/services/storagemanager"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) sm.Store {
		return sm.NewMemoryStore()
	})
}
