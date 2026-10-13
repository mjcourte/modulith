package engine

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"

	"gyoza/internal/engine/fbs"
)

//go:generate flatc --go --gen-object-api --go-module-name gyoza/internal/engine -o . ../../engine/schema/common.fbs ../../engine/schema/delta.fbs ../../engine/schema/snapshot.fbs
//go:generate gofmt -w fbs

// The mock's wire format: flatbuffers from engine/schema, the schema the
// native engine and the frontend's WASM build share.

const mockVersion = 1

// MockSet is one register write in a delta built by MockDelta. Value is an
// int64, float64, string, []byte or AssetRef; Removed deletes the key
// instead (Value is then ignored).
type MockSet struct {
	Key     string
	Value   any
	Removed bool
}

// MockDelta encodes a delta for the mock engine, for tests and tools that
// need to produce edits without a real client. lamport 0 leaves it
// unstamped, so the receiving document stamps it (replica is then ignored).
func MockDelta(replica string, lamport uint64, sets ...MockSet) (DeltaInput, error) {
	d := &fbs.DeltaT{Lamport: lamport}
	if lamport != 0 {
		d.Replica = replica
	}
	for _, s := range sets {
		r := &fbs.RegisterT{Key: s.Key, Removed: s.Removed}
		if !s.Removed {
			v, err := toValue(s.Value)
			if err != nil {
				return DeltaInput{}, fmt.Errorf("engine: mock delta %q: %w", s.Key, err)
			}
			r.Value = v
		}
		d.Sets = append(d.Sets, r)
	}
	return DeltaInput{Format: WireNative, Version: mockVersion, Payload: encodeDelta(d)}, nil
}

func toValue(v any) (*fbs.ValueT, error) {
	switch v := v.(type) {
	case int64:
		return &fbs.ValueT{Type: fbs.ValueIntVal, Value: &fbs.IntValT{V: v}}, nil
	case int:
		return &fbs.ValueT{Type: fbs.ValueIntVal, Value: &fbs.IntValT{V: int64(v)}}, nil
	case float64:
		return &fbs.ValueT{Type: fbs.ValueFloatVal, Value: &fbs.FloatValT{V: v}}, nil
	case string:
		return &fbs.ValueT{Type: fbs.ValueStrVal, Value: &fbs.StrValT{V: v}}, nil
	case []byte:
		return &fbs.ValueT{Type: fbs.ValueBytesVal, Value: &fbs.BytesValT{V: v}}, nil
	case AssetRef:
		return &fbs.ValueT{Type: fbs.ValueAssetRef, Value: &fbs.AssetRefT{AssetId: v.ID, Version: int32(v.Version)}}, nil
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}

// fromValue is toValue's inverse, for JSON views: AssetRef becomes an object.
func fromValue(v *fbs.ValueT) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *fbs.IntValT:
		return x.V
	case *fbs.FloatValT:
		return x.V
	case *fbs.StrValT:
		return x.V
	case *fbs.BytesValT:
		return x.V // encoding/json writes []byte as base64
	case *fbs.AssetRefT:
		return map[string]any{"assetId": x.AssetId, "version": x.Version}
	}
	return nil
}

func assetOf(v *fbs.ValueT) (AssetRef, bool) {
	if v == nil || v.Type != fbs.ValueAssetRef {
		return AssetRef{}, false
	}
	a := v.Value.(*fbs.AssetRefT)
	return AssetRef{ID: a.AssetId, Version: int(a.Version)}, true
}

func encodeDelta(d *fbs.DeltaT) []byte {
	b := flatbuffers.NewBuilder(256)
	fbs.FinishDeltaBuffer(b, d.Pack(b))
	return b.FinishedBytes()
}

func encodeSnapshot(s *fbs.SnapshotT) []byte {
	b := flatbuffers.NewBuilder(1024)
	fbs.FinishSnapshotBuffer(b, s.Pack(b))
	return b.FinishedBytes()
}

// decodeDelta and decodeSnapshot check the format, version and file
// identifier. The Go flatbuffers runtime has no verifier and panics on a
// malformed buffer, so the panic is turned into an error.
func decodeDelta(in DeltaInput) (d *fbs.DeltaT, err error) {
	if err := checkWire("delta", in.Format, in.Version); err != nil {
		return nil, err
	}
	if len(in.Payload) < 8 || !fbs.DeltaBufferHasIdentifier(in.Payload) {
		return nil, fmt.Errorf("%w: not a delta buffer", ErrUnsupportedFormat)
	}
	defer recoverMalformed("delta", &err)
	return fbs.GetRootAsDelta(in.Payload, 0).UnPack(), nil
}

func decodeSnapshot(s Snapshot) (out *fbs.SnapshotT, err error) {
	if err := checkWire("snapshot", s.Format, s.Version); err != nil {
		return nil, err
	}
	if len(s.Payload) < 8 || !fbs.SnapshotBufferHasIdentifier(s.Payload) {
		return nil, fmt.Errorf("%w: not a snapshot buffer", ErrUnsupportedFormat)
	}
	defer recoverMalformed("snapshot", &err)
	return fbs.GetRootAsSnapshot(s.Payload, 0).UnPack(), nil
}

func checkWire(what string, f WireFormat, version int) error {
	if f != WireNative || version != mockVersion {
		return fmt.Errorf("%w: %s %s v%d", ErrUnsupportedFormat, what, f, version)
	}
	return nil
}

func recoverMalformed(what string, err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("engine: malformed %s: %v", what, r)
	}
}
