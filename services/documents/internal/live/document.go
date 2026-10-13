package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gyoza/internal/engine"
)

// ErrNotOpen is returned by a Doc the registry has let go of (evicted or
// closed). A caller only sees it if it kept the Doc past release, or raced an
// eviction: acquiring again loads it afresh.
var ErrNotOpen = errors.New("live: document no longer open")

// Doc is an open document. Read through Engine; change it only through
// Update, so the change is tracked and saved.
type Doc struct {
	id    string
	r     *Registry
	ready chan struct{} // closed once eng (or loadErr) is set
	eng   engine.Document
	// loadErr is set before ready is closed and never changes after.
	loadErr error

	// Guarded by r.mu.
	refs       int
	lastAccess time.Time
	evicting   chan struct{} // non-nil while an eviction is saving; closed when it ends
	ttl        timer

	// saveMu keeps one save in flight per document.
	saveMu sync.Mutex

	// Guarded by mu.
	mu         sync.Mutex
	shutDown   bool
	changes    uint64 // incremented by every Update
	saved      uint64 // value of changes covered by the last stored snapshot
	seq        uint64 // last stream sequence applied
	lastChange time.Time
	lastActor  string    // who made the last change
	dirtySince time.Time // first change not yet saved; zero when clean
	failures   int       // consecutive failed saves
	debounce   timer
}

func (d *Doc) ID() string { return d.id }

// Engine returns the engine document, for reads (queries, renders, asset
// lists). Changes must go through Update.
func (d *Doc) Engine() engine.Document { return d.eng }

// Snapshot returns the document's state with the last stream sequence it
// covers, taken together so no Update lands between them.
func (d *Doc) Snapshot(ctx context.Context) (engine.Snapshot, uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shutDown {
		return engine.Snapshot{}, 0, ErrNotOpen
	}
	snap, err := d.eng.Snapshot(ctx)
	return snap, d.seq, err
}

// Update runs fn, which changes the document, and schedules a save. seq is
// the stream sequence the change came from (0 for none); the document
// remembers the highest it has seen. actorID is who made the change (empty
// if unknown); the next snapshot records the last one. Saves wait while fn runs, so a
// snapshot never splits a change from its sequence.
func (d *Doc) Update(seq uint64, actorID string, fn func(engine.Document) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shutDown {
		return ErrNotOpen
	}
	if err := fn(d.eng); err != nil {
		return err
	}
	d.changes++
	d.seq = max(d.seq, seq)
	now := d.r.clock.Now()
	d.lastChange, d.lastActor = now, actorID
	if d.dirtySince.IsZero() {
		d.dirtySince = now
	}
	d.armDebounceLocked()

	d.r.mu.Lock()
	d.lastAccess = now
	d.r.mu.Unlock()
	return nil
}

// armDebounceLocked (re)sets the save timer to the earlier of "changes have
// been quiet for Debounce" and "the oldest unsaved change is MaxDelay old".
func (d *Doc) armDebounceLocked() {
	cfg := d.r.cfg
	at := d.lastChange.Add(cfg.Debounce)
	if limit := d.dirtySince.Add(cfg.MaxDelay); limit.Before(at) {
		at = limit
	}
	d.armLocked(at.Sub(d.r.clock.Now()))
}

func (d *Doc) armLocked(after time.Duration) {
	if d.debounce != nil {
		d.debounce.Stop()
	}
	d.debounce = d.r.clock.AfterFunc(max(after, 0), d.onDebounce)
}

// onDebounce is the save timer's callback.
func (d *Doc) onDebounce() {
	err := d.saveNow(d.r.ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.shutDown:
	case err != nil:
		// Retry with exponential backoff from Debounce, capped at MaxDelay.
		cfg := d.r.cfg
		backoff := min(cfg.Debounce<<min(d.failures-1, 16), cfg.MaxDelay)
		slog.Error("documents: saving snapshot failed", "project", d.id, "err", err, "retry_in", backoff)
		d.armLocked(backoff)
	case d.changes != d.saved:
		d.armDebounceLocked() // changed while saving
	}
}

// saveNow stores a snapshot if there are unsaved changes. Edits continue
// while it stores: the snapshot is taken under the lock, the I/O outside it.
func (d *Doc) saveNow(ctx context.Context) error {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()

	d.mu.Lock()
	if d.shutDown || d.changes == d.saved {
		d.mu.Unlock()
		return nil
	}
	snap, err := d.eng.Snapshot(ctx)
	changes, seq := d.changes, d.seq
	last := Edit{By: d.lastActor, At: d.lastChange}
	d.mu.Unlock()
	storedSeq := seq
	if err == nil {
		storedSeq, err = d.r.save(ctx, d.id, d.eng, snap, seq, last)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.failures++
		return fmt.Errorf("live: save %s: %w", d.id, err)
	}
	d.failures = 0
	d.saved = changes
	d.seq = max(d.seq, storedSeq)
	if d.changes == changes {
		d.dirtySince = time.Time{}
	} else {
		// The changes after the snapshot are now the oldest unsaved ones;
		// their exact time is gone, so count from the snapshot.
		d.dirtySince = d.r.clock.Now()
	}
	return nil
}

// checkTTL is the TTL timer's callback: evict the document if it's idle
// and not held, else check again when it could next expire.
func (d *Doc) checkTTL() {
	r := d.r
	r.mu.Lock()
	if r.closed || r.docs[d.id] != d {
		r.mu.Unlock()
		return
	}
	if d.refs > 0 {
		d.ttl = r.clock.AfterFunc(r.cfg.TTL, d.checkTTL)
		r.mu.Unlock()
		return
	}
	if idle := r.clock.Now().Sub(d.lastAccess); idle < r.cfg.TTL {
		d.ttl = r.clock.AfterFunc(r.cfg.TTL-idle, d.checkTTL)
		r.mu.Unlock()
		return
	}
	// Block new Acquires while saving: they wait on evicting.
	done := make(chan struct{})
	d.evicting = done
	r.mu.Unlock()

	err := d.saveNow(r.ctx)

	r.mu.Lock()
	d.evicting = nil
	close(done)
	if err != nil {
		slog.Error("documents: saving before eviction failed; keeping it in memory", "project", d.id, "err", err)
		d.ttl = r.clock.AfterFunc(r.cfg.MaxDelay, d.checkTTL)
		r.mu.Unlock()
		return
	}
	delete(r.docs, d.id)
	r.mu.Unlock()
	d.shut()
}

// shut stops the document's timers and closes its engine document. The
// caller has made sure everything is saved (or given up on it).
func (d *Doc) shut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shutDown {
		return
	}
	d.shutDown = true
	if d.debounce != nil {
		d.debounce.Stop()
	}
	if err := d.eng.Close(); err != nil {
		slog.Error("documents: closing engine document", "project", d.id, "err", err)
	}
}
