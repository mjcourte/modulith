package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mjcourte/modulith/internal/engine/fbs"
)

// A crafted snapshot whose clock looks honest but which hides one register
// stamped in the future is refused too. Honest snapshots can't look like
// this (their clock is at least every stamp), so it needs building by hand.
func TestMockRejectsForgedRegisterInSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	d, _ := Mock{}.New(context.Background(), Settings{ID: "doc", Now: func() time.Time { return now }})
	forged := &fbs.SnapshotT{
		Clock: HLC(now),
		Registers: []*fbs.RegisterT{{
			Key:   "k",
			Value: &fbs.ValueT{Type: fbs.ValueStrVal, Value: &fbs.StrValT{V: "forged"}},
			Stamp: &fbs.StampT{Lamport: HLC(now.Add(time.Hour)), Replica: "attacker"},
		}},
	}
	snap := Snapshot{Format: WireNative, Version: mockVersion, Payload: encodeSnapshot(forged)}
	if err := d.Merge(context.Background(), snap); !errors.Is(err, ErrBadStamp) {
		t.Fatalf("got %v, want ErrBadStamp", err)
	}
}
