package nats_test

import (
	"errors"
	"testing"

	"github.com/mjcourte/modulith/platform/nats"
)

func TestSubjects(t *testing.T) {
	tests := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"delta", func() (string, error) { return nats.DocumentDelta("p1") }, "document.p1.delta"},
		{"snapshot", func() (string, error) { return nats.DocumentSnapshot("p1") }, "document.p1.snapshot"},
		{"viewing", func() (string, error) { return nats.UserStateViewing("p1", "alice") }, "userstate.p1.alice.viewing"},
		{"cursor", func() (string, error) { return nats.UserStateCursor("p1", "alice") }, "userstate.p1.alice.cursorpos"},
		{"userstate all", func() (string, error) { return nats.UserStateAll("p1") }, "userstate.p1.>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestSubjectsRejectBadTokens(t *testing.T) {
	for _, id := range []string{"", "*", ">", "a.b", "a b", "a\tb", "a>"} {
		if s, err := nats.DocumentDelta(id); !errors.Is(err, nats.ErrInvalidToken) {
			t.Errorf("DocumentDelta(%q) = %q, %v; want ErrInvalidToken", id, s, err)
		}
		// The user ID is checked too, not just the first ID.
		if s, err := nats.UserStateCursor("p1", id); !errors.Is(err, nats.ErrInvalidToken) {
			t.Errorf("UserStateCursor(p1, %q) = %q, %v; want ErrInvalidToken", id, s, err)
		}
	}
}
