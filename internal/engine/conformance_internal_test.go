package engine

// The conformance walker. Cases live in engine/conformance/cases and are the
// CRDT's specification: the Rust engine walks the same directory and must
// agree. See engine/conformance/README.md for the format.
//
// Run `go test ./internal/engine -bless` to write the expected.* files from
// the current implementation, then read the diff before committing it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/mjcourte/modulith/internal/engine/fbs"
)

var confBless = flag.Bool("bless", false, "write conformance expected.* files from the current implementation")

const confCasesDir = "../../engine/conformance/cases"

// confPermuteMax caps the permutation explosion: 6 ops is already 720 orders.
const confPermuteMax = 6

type confCase struct {
	Doc      string       `json:"doc"`
	Settings confSettings `json:"settings"`
	Permute  bool         `json:"permute"`
	Ops      []confOp     `json:"ops"`
	Expect   confExpect   `json:"expect"`
	// Skip names the implementations a case does not bind, each with the
	// reason. Absent means every implementation runs it, which is the point
	// of the vectors; a reason is mandatory so a skip has to be argued for
	// rather than reached for.
	Skip map[string]string `json:"skip"`
}

// confImpl is this walker's key in a case's skip map.
const confImpl = "go"

// confImpls is every implementation that walks these cases. A skip for
// anything else is a typo, and a typo'd skip would run the case it meant to
// exclude — or, worse, exclude nothing and look like it had.
var confImpls = []string{"go", "rust"}

type confSettings struct {
	ID      string `json:"id"`
	Replica string `json:"replica"`
	Now     string `json:"now"`
	MaxSkew string `json:"maxSkew"`
}

type confExpect struct {
	Snapshot string `json:"snapshot"`
	View     string `json:"view"`
}

type confOp struct {
	Op       string         `json:"op"`
	At       string         `json:"at"`
	Error    string         `json:"error"`
	Sets     []confRegister `json:"sets"`
	Stamp    *confStamp     `json:"stamp"`
	Trusted  bool           `json:"trusted"`
	Snapshot *confSnapshot  `json:"snapshot"`
	From     string         `json:"from"`
	Save     string         `json:"save"`
	Format   string         `json:"format"` // query: default native
	IDs      []string       `json:"ids"`    // assets: the filter
	// RawSnapshot is base64 bytes handed to merge/revert unchanged, for cases
	// about buffers no encoder would produce. Version overrides the wire
	// version, for the same reason.
	RawSnapshot string `json:"rawSnapshot"`
	Version     *int   `json:"version"`
}

type confRegister struct {
	Key    string     `json:"key"`
	Value  *confValue `json:"value"`
	Remove bool       `json:"remove"`
	Stamp  *confStamp `json:"stamp"`
	// NoValue writes a register with the union unset and removed false, which
	// no honest encoder produces. Only meaningful inside a hand-written
	// snapshot: MockDelta cannot express it.
	NoValue bool `json:"noValue"`
}

type confSnapshot struct {
	Clock     confLamport    `json:"clock"`
	Registers []confRegister `json:"registers"`
}

// confStamp is {"lamport": "<decimal>"} or {"hlcOf": "<rfc3339>"}, plus an
// optional plusCounter to reach into the HLC's counter bits.
//
// The lamport fields are repeated rather than embedded: embedding confLamport
// would promote its UnmarshalJSON to this type and decode only that part,
// silently dropping Replica.
type confStamp struct {
	Lamport     string `json:"lamport"`
	HLCOf       string `json:"hlcOf"`
	PlusCounter uint64 `json:"plusCounter"`
	Replica     string `json:"replica"`
}

func (s confStamp) value() (uint64, error) {
	return confLamport{Lamport: s.Lamport, HLCOf: s.HLCOf, PlusCounter: s.PlusCounter}.value()
}

type confLamport struct {
	Lamport     string `json:"lamport"`
	HLCOf       string `json:"hlcOf"`
	PlusCounter uint64 `json:"plusCounter"`
}

// UnmarshalJSON accepts a bare decimal string as well as the object form, so
// {"clock": "123"} and {"clock": {"hlcOf": "..."}} both work.
func (c *confLamport) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &c.Lamport)
	}
	type raw confLamport // avoid recursing into this method
	return json.Unmarshal(b, (*raw)(c))
}

func (c confLamport) value() (uint64, error) {
	switch {
	case c.Lamport != "" && c.HLCOf != "":
		return 0, fmt.Errorf("stamp has both lamport and hlcOf")
	case c.Lamport != "":
		return strconv.ParseUint(c.Lamport, 10, 64)
	case c.HLCOf != "":
		t, err := time.Parse(time.RFC3339, c.HLCOf)
		if err != nil {
			return 0, fmt.Errorf("hlcOf: %w", err)
		}
		return HLC(t) + c.PlusCounter, nil
	}
	return 0, nil
}

// confValue is a one-key object naming the union type.
type confValue struct {
	Int       *string       `json:"int"`
	Float     *float64      `json:"float"`
	FloatBits *string       `json:"floatBits"`
	Str       *string       `json:"str"`
	Bytes     *string       `json:"bytes"`
	AssetRef  *confAssetRef `json:"assetRef"`
}

type confAssetRef struct {
	AssetID string `json:"assetId"`
	Version int32  `json:"version"`
}

// any converts the value for MockDelta, which takes the same Go types a
// caller would write.
func (v *confValue) any() (any, error) {
	switch {
	case v == nil:
		return nil, fmt.Errorf("register has no value")
	case v.Int != nil:
		return strconv.ParseInt(*v.Int, 10, 64)
	case v.Float != nil:
		return *v.Float, nil
	case v.FloatBits != nil:
		bits, err := strconv.ParseUint(strings.TrimPrefix(*v.FloatBits, "0x"), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("floatBits: %w", err)
		}
		return math.Float64frombits(bits), nil
	case v.Str != nil:
		return *v.Str, nil
	case v.Bytes != nil:
		return base64.StdEncoding.DecodeString(*v.Bytes)
	case v.AssetRef != nil:
		return AssetRef{ID: v.AssetRef.AssetID, Version: int(v.AssetRef.Version)}, nil
	}
	return nil, fmt.Errorf("value names no union type")
}

// fbs builds the wire representation, for registers inside a hand-written
// snapshot (which MockDelta can't express: it stamps a whole delta, not a
// register).
func (v *confValue) fbs() (*fbs.ValueT, error) {
	x, err := v.any()
	if err != nil {
		return nil, err
	}
	return toValue(x)
}

func TestConformance(t *testing.T) {
	entries, err := os.ReadDir(confCasesDir)
	if err != nil {
		t.Fatalf("cases: %v", err)
	}
	var cases, skipped int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cases++
		t.Run(e.Name(), func(t *testing.T) {
			dir := filepath.Join(confCasesDir, e.Name())
			c := confLoadCase(t, dir)
			// Count before skipping: t.Skipf ends the goroutine, so nothing
			// after it in this closure runs.
			if why, ok := c.Skip[confImpl]; ok {
				skipped++
				t.Skipf("not binding on %s: %s", confImpl, why)
			}
			confRunCase(t, dir, c)
		})
	}
	if cases == 0 {
		t.Fatal("no cases found: the spec can't be empty")
	}
	if cases == skipped {
		t.Fatal("every case skipped: the suite proved nothing")
	}
	t.Logf("%d cases, %d run, %d skipped for %s", cases, cases-skipped, skipped, confImpl)
}

// confLoadCase reads and validates ops.json. It checks every implementation's
// skip, not just this one's: one walker catching another's typo is most of
// what a shared suite is for.
func confLoadCase(t *testing.T, dir string) confCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "ops.json"))
	if err != nil {
		t.Fatalf("ops.json: %v", err)
	}
	var c confCase
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // a typo'd field must not silently weaken a case
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("ops.json: %v", err)
	}
	if c.Settings.Replica == "" || c.Settings.Now == "" {
		t.Fatal("settings must state replica and now: a generated ID or a real clock makes the case unreproducible")
	}
	for impl, why := range c.Skip {
		if !slices.Contains(confImpls, impl) {
			t.Fatalf("skip names unknown implementation %q (want one of %v)", impl, confImpls)
		}
		if strings.TrimSpace(why) == "" {
			t.Fatalf("skip[%q] has no reason: a case excluded without one is a case nobody can reinstate", impl)
		}
	}
	for _, op := range c.Ops {
		if op.Error != "" && confErrors[op.Error] == nil {
			t.Fatalf("op %q expects unknown error %q", op.Op, op.Error)
		}
	}
	return c
}

func confRunCase(t *testing.T, dir string, c confCase) {
	t.Helper()
	orders := [][]int{confIdentity(len(c.Ops))}
	if c.Permute {
		if err := confPermutable(c.Ops); err != nil {
			t.Fatalf("permute: %v", err)
		}
		orders = confPermutations(t, len(c.Ops))
	}

	var first []byte
	for _, order := range orders {
		snap, view, err := confApply(&c, order)
		if err != nil {
			t.Fatalf("order %v: %v", order, err)
		}
		if first == nil {
			first = snap
			confCheckFile(t, dir, c.Expect.Snapshot, "expected.snapshot", snap)
			if c.Expect.View != "" {
				confCheckFile(t, dir, c.Expect.View, "expected.view.json", view)
			}
			continue
		}
		if string(snap) != string(first) {
			t.Errorf("order %v produced different bytes: replicas would diverge", order)
		}
	}
}

// confApply runs the case's ops in the given order and returns the final
// snapshot bytes, and the last query's view if there was one.
func confApply(c *confCase, order []int) (snap, view []byte, err error) {
	base, err := time.Parse(time.RFC3339, c.Settings.Now)
	if err != nil {
		return nil, nil, fmt.Errorf("settings.now: %w", err)
	}
	var skew time.Duration
	if c.Settings.MaxSkew != "" {
		if skew, err = time.ParseDuration(c.Settings.MaxSkew); err != nil {
			return nil, nil, fmt.Errorf("settings.maxSkew: %w", err)
		}
	}
	now := base
	ctx := context.Background()
	d, err := Mock{}.New(ctx, Settings{
		ID:      c.Settings.ID,
		Replica: c.Settings.Replica,
		Now:     func() time.Time { return now },
		MaxSkew: skew,
	})
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()

	saved := map[string]Snapshot{}
	for _, i := range order {
		op := c.Ops[i]
		now = base
		if op.At != "" {
			if now, err = time.Parse(time.RFC3339, op.At); err != nil {
				return nil, nil, fmt.Errorf("op %d: at: %w", i, err)
			}
		}
		res, opErr, err := confRunOp(ctx, d, op, saved)
		if err != nil {
			return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Op, err)
		}
		if res != nil {
			view = res
		}
		switch {
		case op.Error == "" && opErr != nil:
			return nil, nil, fmt.Errorf("op %d (%s): unexpected error: %v", i, op.Op, opErr)
		case op.Error != "" && opErr == nil:
			return nil, nil, fmt.Errorf("op %d (%s): want error %q, got nil", i, op.Op, op.Error)
		case op.Error != "" && !confMatchError(op.Error, opErr):
			return nil, nil, fmt.Errorf("op %d (%s): want error %q, got %v", i, op.Op, op.Error, opErr)
		}
	}
	s, err := d.Snapshot(ctx)
	if err != nil {
		// A closed document cannot be snapshotted, so a case that closes it
		// expects no snapshot of its own.
		if errors.Is(err, ErrClosed) {
			return nil, view, nil
		}
		return nil, nil, err
	}
	return s.Payload, view, nil
}

// confRunOp performs one op. res carries a query's view, if any. opErr is the
// engine's error, which a case may expect; caseErr reports a malformed case.
func confRunOp(ctx context.Context, d Document, op confOp, saved map[string]Snapshot) (res []byte, opErr error, caseErr error) {
	switch op.Op {
	case "edit":
		var lamport uint64
		var replica string
		if op.Stamp != nil {
			l, err := op.Stamp.value()
			if err != nil {
				return nil, nil, err
			}
			lamport, replica = l, op.Stamp.Replica
		}
		sets := make([]MockSet, 0, len(op.Sets))
		for _, r := range op.Sets {
			s := MockSet{Key: r.Key, Removed: r.Remove}
			if !r.Remove {
				v, err := r.Value.any()
				if err != nil {
					return nil, nil, err
				}
				s.Value = v
			}
			sets = append(sets, s)
		}
		in, err := MockDelta(replica, lamport, sets...)
		if err != nil {
			return nil, err, nil // a value MockDelta refuses is an engine answer
		}
		in.Trusted = op.Trusted
		_, engErr := d.Edit(ctx, in)
		return nil, engErr, nil

	case "merge", "revert":
		s, err := confSnapshotArg(op, saved)
		if err != nil {
			return nil, nil, err
		}
		if op.Op == "merge" {
			return nil, d.Merge(ctx, s), nil
		}
		_, engErr := d.RevertTo(ctx, s)
		return nil, engErr, nil

	case "snapshot":
		s, engErr := d.Snapshot(ctx)
		if engErr == nil && op.Save != "" {
			saved[op.Save] = s
		}
		return nil, engErr, nil

	case "query":
		format := QueryFormat(op.Format)
		if format == "" {
			format = QueryNative
		}
		v, engErr := d.Query(ctx, Query{Format: format})
		if engErr != nil {
			return nil, engErr, nil
		}
		return v.Data, nil, nil

	case "assets":
		_, engErr := d.Assets(ctx, AssetFilter{IDs: op.IDs})
		return nil, engErr, nil

	case "close":
		return nil, d.Close(), nil
	}
	return nil, nil, fmt.Errorf("unknown op %q", op.Op)
}

func confSnapshotArg(op confOp, saved map[string]Snapshot) (Snapshot, error) {
	version := mockVersion
	if op.Version != nil {
		version = *op.Version
	}
	switch {
	case op.From != "":
		s, ok := saved[op.From]
		if !ok {
			return Snapshot{}, fmt.Errorf("no saved snapshot %q", op.From)
		}
		s.Version = version
		return s, nil
	case op.RawSnapshot != "":
		b, err := base64.StdEncoding.DecodeString(op.RawSnapshot)
		if err != nil {
			return Snapshot{}, fmt.Errorf("rawSnapshot: %w", err)
		}
		return Snapshot{Format: WireNative, Version: version, Payload: b}, nil
	case op.Snapshot != nil:
		return confPackSnapshot(op.Snapshot, version)
	}
	return Snapshot{}, fmt.Errorf("op needs from, snapshot or rawSnapshot")
}

// confPackSnapshot builds a snapshot by hand, so a case can hold one no
// honest replica would produce (a register stamped above the clock).
func confPackSnapshot(cs *confSnapshot, version int) (Snapshot, error) {
	clock, err := cs.Clock.value()
	if err != nil {
		return Snapshot{}, fmt.Errorf("clock: %w", err)
	}
	out := &fbs.SnapshotT{Clock: clock, Registers: []*fbs.RegisterT{}}
	for _, r := range cs.Registers {
		reg := &fbs.RegisterT{Key: r.Key, Removed: r.Remove}
		if !r.Remove && !r.NoValue {
			if reg.Value, err = r.Value.fbs(); err != nil {
				return Snapshot{}, fmt.Errorf("register %q: %w", r.Key, err)
			}
		}
		if r.Stamp != nil {
			l, err := r.Stamp.value()
			if err != nil {
				return Snapshot{}, fmt.Errorf("register %q stamp: %w", r.Key, err)
			}
			reg.Stamp = &fbs.StampT{Lamport: l, Replica: r.Stamp.Replica}
		}
		out.Registers = append(out.Registers, reg)
	}
	b := flatbuffers.NewBuilder(1024)
	fbs.FinishSnapshotBuffer(b, out.Pack(b))
	return Snapshot{Format: WireNative, Version: version, Payload: b.FinishedBytes()}, nil
}

// confErrors maps every stable error name in engine/conformance's table onto
// this implementation's sentinel. The map is the whole vocabulary: confLoadCase
// rejects a case naming anything outside it, so a misspelled expectation fails
// loudly instead of matching nothing. Matching is errors.Is throughout — a
// message substring would let a reworded error silently stop proving its case.
var confErrors = map[string]error{
	"bad_stamp":          ErrBadStamp,
	"unsupported_format": ErrUnsupportedFormat,
	"not_found":          ErrNotFound,
	"closed":             ErrClosed,
	"malformed":          ErrMalformed,
	"no_key":             ErrNoKey,
	"no_value":           ErrNoValue,
	"not_finite":         ErrNotFinite,
	"unstamped_register": ErrUnstampedRegister,
	"unstamped_replica":  ErrUnstampedReplica,
}

func confMatchError(name string, err error) bool {
	return errors.Is(err, confErrors[name])
}

// confCheckFile compares got against the blessed file, or writes it under
// -bless. A missing file fails: an unblessed case must not pass vacuously.
func confCheckFile(t *testing.T, dir, name, fallback string, got []byte) {
	t.Helper()
	if name == "" {
		name = fallback
	}
	path := filepath.Join(dir, name)
	if *confBless {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("bless %s: %v", name, err)
		}
		t.Logf("blessed %s (%d bytes)", name, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("%s is missing: run `go test ./internal/engine -bless` and review the diff", name)
		}
		t.Fatalf("%s: %v", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs\n got %d bytes: %x\nwant %d bytes: %x", name, len(got), got, len(want), want)
	}
}

// confPermutable reports whether every op carries its own stamp. An unstamped
// local edit takes a fresh one, so its result legitimately depends on order.
func confPermutable(ops []confOp) error {
	for i, op := range ops {
		switch op.Op {
		case "merge":
		case "edit":
			if op.Stamp == nil {
				return fmt.Errorf("op %d is an unstamped edit", i)
			}
		default:
			return fmt.Errorf("op %d (%s) is not reorderable", i, op.Op)
		}
	}
	return nil
}

func confIdentity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func confPermutations(t *testing.T, n int) [][]int {
	t.Helper()
	if n > confPermuteMax {
		t.Fatalf("permute: %d ops is too many (max %d)", n, confPermuteMax)
	}
	var out [][]int
	var rec func(cur, rest []int)
	rec = func(cur, rest []int) {
		if len(rest) == 0 {
			out = append(out, slices.Clone(cur))
			return
		}
		for i := range rest {
			next := slices.Concat(rest[:i:i], rest[i+1:])
			rec(append(cur, rest[i]), next)
		}
	}
	rec(nil, confIdentity(n))
	return out
}
