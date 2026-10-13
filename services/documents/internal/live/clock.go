package live

import "time"

// clock is the time source for the registry's timers, so tests can replace
// it with a fake they advance by hand (export_test.go).
type clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine after d, like time.AfterFunc.
	AfterFunc(d time.Duration, f func()) timer
}

type timer interface {
	// Stop prevents the call if it hasn't started; it doesn't wait for one
	// that has, so callbacks must tolerate running late.
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }
