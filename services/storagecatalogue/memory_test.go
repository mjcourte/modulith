package storagecatalogue_test

import (
	"testing"

	catalogue "gyoza/services/storagecatalogue"
)

func TestMemory(t *testing.T) {
	runServiceTests(t, func(*testing.T) catalogue.Service {
		return catalogue.NewMemory()
	})
}
