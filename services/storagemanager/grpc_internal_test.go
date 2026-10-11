package storagemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// overWire sends err through the server's and the client's error mapping, as
// a real call would.
func overWire(err error) error { return fromStatus(toStatus(err)) }

// Every sentinel survives the trip as itself, and only as itself (several
// share a status code, so the code alone couldn't tell them apart).
func TestWireErrorsRoundTrip(t *testing.T) {
	for _, w := range wireErrors {
		t.Run(w.reason, func(t *testing.T) {
			sent := fmt.Errorf("doing something: %w", w.err)
			got := overWire(sent)
			if !errors.Is(got, w.err) {
				t.Fatalf("got %v, want it to match %v", got, w.err)
			}
			if status.Code(toStatus(sent)) != w.code {
				t.Errorf("code = %v, want %v", status.Code(toStatus(sent)), w.code)
			}
			if got.Error() != sent.Error() {
				t.Errorf("message = %q, want the server's %q", got.Error(), sent.Error())
			}
			for _, other := range wireErrors {
				if other.err != w.err && errors.Is(got, other.err) {
					t.Errorf("also matches %v", other.err)
				}
			}
		})
	}
}

func TestWireErrorsSpecialCases(t *testing.T) {
	// The counts a UI shows ("delete 3 folders and 12 files?") come along.
	got := overWire(fmt.Errorf("delete: %w", &FolderNotEmptyError{Folders: 3, Assets: 12}))
	var notEmpty *FolderNotEmptyError
	if !errors.As(got, &notEmpty) || notEmpty.Folders != 3 || notEmpty.Assets != 12 || !errors.Is(got, ErrFolderNotEmpty) {
		t.Errorf("folder not empty arrived as %#v (%v)", notEmpty, got)
	}

	// An error matching two sentinels still matches both.
	got = overWire(fmt.Errorf("%w: %w", ErrInvalidUpload, ErrInvalidAsset))
	if !errors.Is(got, ErrInvalidUpload) || !errors.Is(got, ErrInvalidAsset) {
		t.Errorf("double error arrived as %v, want both sentinels", got)
	}

	// Anything else is internal, and its details stay on the server.
	secret := errors.New(`pq: relation "storagemanager.assets" does not exist`)
	sent := toStatus(fmt.Errorf("list: %w", secret))
	if status.Code(sent) != codes.Internal || strings.Contains(sent.Error(), "relation") {
		t.Errorf("unexpected error sent as %v, want a bare internal error", sent)
	}

	// Cancellation stays recognisable, so callers can tell it from a failure.
	if got := overWire(fmt.Errorf("x: %w", context.Canceled)); !errors.Is(got, context.Canceled) {
		t.Errorf("cancellation arrived as %v", got)
	}
	if got := overWire(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("deadline arrived as %v", got)
	}
}
