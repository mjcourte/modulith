package dam_test

import (
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) dam.Store {
		return dam.NewMemoryStore()
	})
}
