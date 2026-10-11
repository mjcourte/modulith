package storageledger_test

import (
	"testing"

	ledger "gyoza/services/storageledger"
)

func TestMemory(t *testing.T) {
	runServiceTests(t, func(*testing.T) ledger.Service {
		return ledger.NewMemory()
	})
}
