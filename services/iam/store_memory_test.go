package iam_test

import (
	"testing"

	"gyoza/services/iam"
)

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(*testing.T) iam.Store { return iam.NewMemoryStore() })
}
