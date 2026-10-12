package dam_test

import (
	"gyoza/services/storage/internal/dam"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) dam.Store {
		return dam.NewMemoryStore()
	})
}
