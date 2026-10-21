package shutdown_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mjcourte/modulith/platform/shutdown"
)

func TestWithinReturnsWhenStopFinishes(t *testing.T) {
	called := false
	err := shutdown.Within(t.Context(), func() { called = true })
	if err != nil {
		t.Errorf("Within = %v, want nil", err)
	}
	if !called {
		t.Error("stop was not called")
	}
}

func TestWithinGivesUpAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block) // let the stuck goroutine finish when the test ends

	start := time.Now()
	err := shutdown.Within(ctx, func() { <-block }) // never returns on its own
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Within = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Within took %v, want it to return at the deadline", elapsed)
	}
}
