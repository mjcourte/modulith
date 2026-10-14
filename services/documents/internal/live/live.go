package live

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gyoza/internal/engine"
)

// ErrClosed is returned by Acquire after Close.
var ErrClosed = errors.New("live: registry closed")

// Loader opens a project's document: from its latest snapshot, or a new
// one if it has none. seq is the last stream sequence the state covers.
type Loader func(ctx context.Context, projectID string) (doc engine.Document, seq uint64, err error)

// Edit is the last change a snapshot holds: who made it (empty if unknown,
// e.g. a delta without an actor) and when. Storage records it like a `touch`.
type Edit struct {
	By string
	At time.Time
}

// Saver stores a snapshot of a project covering stream sequence seq, and
// returns the sequence the stored snapshot covers. That can be later than
// seq: when a newer snapshot is already stored, the Saver merges it into doc
// and stores the merge instead, and the document then covers the newer
// sequence too. It is called with no registry lock held (doc is safe for
// concurrent use), at most once at a time per project.
type Saver func(ctx context.Context, projectID string, doc engine.Document, snap engine.Snapshot, seq uint64, last Edit) (uint64, error)

// Config holds the timings; zero fields take the defaults.
type Config struct {
	// Debounce: a save waits until changes have stopped for this long.
	Debounce time.Duration
	// MaxDelay caps how long a change can wait to be saved while edits
	// keep coming. It is also the longest retry delay after failures.
	MaxDelay time.Duration
	// TTL: a document nobody has accessed for this long is saved and
	// dropped from memory.
	TTL time.Duration
}

func (c Config) withDefaults() Config {
	if c.Debounce == 0 {
		c.Debounce = 2 * time.Second
	}
	if c.MaxDelay == 0 {
		c.MaxDelay = 30 * time.Second
	}
	if c.TTL == 0 {
		c.TTL = 10 * time.Minute
	}
	return c
}

// Registry holds the open documents.
//
// Locking: Registry.mu guards the map and each Doc's membership fields
// (refs, lastAccess, eviction state, ttl timer); Doc.mu guards the document's
// change tracking. Doc.mu may be taken before Registry.mu (Update does), never
// after: nothing holding Registry.mu waits for a Doc.mu.
type Registry struct {
	load  Loader
	save  Saver
	cfg   Config
	clock clock
	// ctx is for saves started by timers; Close cancels it after its own
	// final saves.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	docs   map[string]*Doc
	closed bool
}

func New(load Loader, save Saver, cfg Config) *Registry {
	return newRegistry(load, save, cfg, realClock{})
}

func newRegistry(load Loader, save Saver, cfg Config, c clock) *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		load: load, save: save, cfg: cfg.withDefaults(), clock: c,
		ctx: ctx, cancel: cancel,
		docs: map[string]*Doc{},
	}
}

// Acquire returns a project's document, loading it if it isn't in memory,
// and a release function to call when done (it's safe to call twice). A held
// document is never evicted. Concurrent callers for one project share a
// single load, which runs with the first caller's ctx: if that fails, they
// all get its error.
func (r *Registry) Acquire(ctx context.Context, projectID string) (*Doc, func(), error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, nil, ErrClosed
		}
		d, ok := r.docs[projectID]
		switch {
		case !ok:
			d = &Doc{id: projectID, r: r, ready: make(chan struct{}), refs: 1, lastAccess: r.clock.Now()}
			r.docs[projectID] = d
			r.mu.Unlock()
			if err := r.open(ctx, d); err != nil {
				return nil, nil, err
			}
			return d, r.releaser(d), nil

		case d.evicting != nil:
			// It's being saved on its way out: wait, then look again (it
			// either stayed or must be loaded afresh).
			done := d.evicting
			r.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}

		default:
			d.refs++
			d.lastAccess = r.clock.Now()
			r.mu.Unlock()
			release := r.releaser(d)
			select {
			case <-d.ready:
			case <-ctx.Done():
				release()
				return nil, nil, ctx.Err()
			}
			if d.loadErr != nil {
				release()
				return nil, nil, d.loadErr
			}
			return d, release, nil
		}
	}
}

// open runs the load for a new entry and publishes the result to waiters.
func (r *Registry) open(ctx context.Context, d *Doc) error {
	eng, seq, err := r.load(ctx, d.id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		d.loadErr = fmt.Errorf("live: load %s: %w", d.id, err)
		delete(r.docs, d.id)
		close(d.ready)
		return d.loadErr
	}
	d.eng, d.seq = eng, seq
	if !r.closed { // else Close is waiting on ready and will shut it
		d.ttl = r.clock.AfterFunc(r.cfg.TTL, d.checkTTL)
	}
	close(d.ready)
	return nil
}

func (r *Registry) releaser(d *Doc) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			d.refs--
			d.lastAccess = r.clock.Now()
			r.mu.Unlock()
		})
	}
}

// Close saves every document with unsaved changes and closes them all.
// Acquire fails afterwards. It returns the save errors, if any; documents
// whose save failed are closed anyway (their deltas stay in the stream).
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	var docs []*Doc
	for _, d := range r.docs {
		if d.ttl != nil {
			d.ttl.Stop()
		}
		docs = append(docs, d)
	}
	clear(r.docs)
	r.mu.Unlock()

	var errs []error
	for _, d := range docs {
		select { // a load in progress finishes first
		case <-d.ready:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("live: close: load of %s still running: %w", d.id, ctx.Err()))
			continue
		}
		if d.loadErr != nil {
			continue
		}
		if err := d.saveNow(ctx); err != nil {
			errs = append(errs, err)
		}
		d.shut()
	}
	r.cancel()
	return errors.Join(errs...)
}

// Len returns how many documents are loaded.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.docs)
}
