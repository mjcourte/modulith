package live

import (
	"sync"
	"time"
)

// NewWithFakeClock returns a registry whose timers only fire when the test
// advances the returned clock.
func NewWithFakeClock(load Loader, save Saver, cfg Config) (*Registry, *FakeClock) {
	c := &FakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return newRegistry(load, save, cfg, c), c
}

// FakeClock runs timer callbacks synchronously inside Advance, in time
// order, so a test sees their effects as soon as Advance returns.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *FakeClock
	at   time.Time
	f    func()
	done bool // fired or stopped
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) AfterFunc(d time.Duration, f func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

// Advance moves time forward by d, firing every timer that falls due,
// including ones the callbacks set up along the way.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.done && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.done = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}
