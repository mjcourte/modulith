package ledger_test

import (
	"testing"

	ledger "gyoza/services/storage/internal/ledger"
)

func TestMemory(t *testing.T) {
	runServiceTests(t, func(*testing.T) ledger.Service {
		return ledger.NewMemory()
	})
}
