package ledger_test

import (
	"testing"

	ledger "github.com/mjcourte/modulith/services/storage/internal/ledger"
)

func TestOwnerValidate(t *testing.T) {
	tests := []struct {
		owner   ledger.Owner
		wantErr bool
	}{
		{ledger.Owner{Kind: ledger.OwnerUser, ID: "u1"}, false},
		{ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}, false},
		{ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}, false},
		{ledger.Owner{Kind: ledger.OwnerModulith}, false},
		{ledger.Owner{}, true},                                 // not set
		{ledger.Owner{Kind: ledger.OwnerUser}, true},           // missing ID
		{ledger.Owner{Kind: ledger.OwnerModulith, ID: "x"}, true}, // modulith with an ID
		{ledger.Owner{Kind: "team", ID: "t1"}, true},           // unknown kind
	}
	for _, tt := range tests {
		if err := tt.owner.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("%+v.Validate() = %v, wantErr %v", tt.owner, err, tt.wantErr)
		}
	}
}
