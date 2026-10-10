// Package shutdown helps a binary stop within a deadline.
package shutdown

import (
	"context"
	"fmt"
)

// Within calls stop and waits for it to return, or for ctx to end, whichever
// comes first. Use it for cleanups that block without taking a context, such
// as pgxpool.Pool.Close or grpc.Server.GracefulStop, so a stuck connection or
// request can't hang the process forever.
//
// If the deadline passes, stop keeps running in the background and Within
// returns the context's error; the process is about to exit anyway.
func Within(ctx context.Context, stop func()) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("shutdown did not finish in time: %w", ctx.Err())
	}
}
