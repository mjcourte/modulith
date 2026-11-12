# Conformance vectors

The CRDT's specification. Neither implementation owns it: the Go mock
(`internal/engine`) and the Rust engine (`engine/core`) each walk `cases/` and
must agree with every case. Adding a case constrains both at once.

This is the repo's one-shared-suite convention (`runServiceTests`,
`blobtest.RunSuite`) pointed across a language boundary. The oracle is the wire
format's own promise: converged replicas produce byte-identical snapshots
([snapshot.fbs](../schema/snapshot.fbs)), so comparing output bytes tests
convergence directly rather than by proxy.

```
cases/<name>/
  ops.json            # the case: settings, operations, expectations
  expected.snapshot   # blessed flatbuffer bytes after the last op
  expected.view.json  # blessed View bytes, only for cases that query
```

## Determinism

Nothing in a case may depend on the host. Every case states its `replica` and
its `now`; the engine's `Settings.Replica` and `Settings.Now` exist for this.
A generated replica ID or a real clock makes tie-break cases unreproducible,
which is most of what is being pinned here.

64-bit values are **decimal strings** in JSON, never numbers — lamports and
clocks run past 2^53, the same reason `View` encodes them as strings. A case
that writes `"lamport": 117605757353984000` is wrong even when it appears to
work.

## ops.json

```jsonc
{
  "doc": "one line: what this case pins",
  "settings": {
    "id": "doc",                        // required
    "replica": "LOCAL",                 // required; never generated
    "now": "2026-10-12T09:00:00Z",      // required, RFC 3339
    "maxSkew": "1m"                     // optional, default DefaultMaxSkew
  },
  "permute": false,                     // optional, see below
  "ops": [ /* see below */ ],
  "expect": {
    "snapshot": "expected.snapshot",    // optional, this is the default
    "view": "expected.view.json"        // only with a query op
  }
}
```

### Operations

Each op is an object with `"op"` and, optionally, `"at"` (RFC 3339) to move the
wall clock for that op only; the default is `settings.now`. Any op may carry
`"error"`, naming the error it must fail with — the op then has no effect.

| op | fields | notes |
| --- | --- | --- |
| `edit` | `sets`, `stamp?`, `trusted?` | no `stamp` means an unstamped local edit, which the document stamps itself |
| `merge` | `snapshot` or `from` | |
| `revert` | `snapshot` or `from` | |
| `snapshot` | `save?` | `save: "s1"` keeps the result for a later `from: "s1"` |
| `query` | | compared against `expect.view` |

### Values

A value is a one-key object; the key is the union type:

```jsonc
{"int": "-12"}                              // decimal string: int64
{"float": 1.5}                              // JSON number
{"floatBits": "0x8000000000000000"}         // exact bits, for -0.0 and friends
{"str": "hi"}
{"bytes": "aGk="}                           // base64, standard alphabet
{"assetRef": {"assetId": "a1", "version": 3}}
```

A register in `sets` or in an inline `snapshot`:

```jsonc
{"key": "k", "value": {"str": "a"}}
{"key": "k", "remove": true}                             // tombstone
{"key": "k", "value": {"int": "1"}, "stamp": {...}}      // snapshots only
```

### Stamps

```jsonc
{"lamport": "117605757353984000", "replica": "B"}   // exact
{"hlcOf": "2026-10-12T10:00:00Z", "replica": "B"}   // HLC(t), readable
```

`hlcOf` is there because hand-writing HLC decimals is unreadable and easy to
get wrong; prefer it unless the case is about an exact value. A case may add
`"plusCounter": n` to `hlcOf` to reach into the counter bits.

### Inline snapshots

`snapshot` is `{"clock": "<decimal>", "registers": [ ... ]}` with every
register stamped. Snapshots are written by hand so a case can build one no
honest replica would produce — that is how the forged-stamp cases work.

### permute

`"permute": true` makes the runner apply the ops in every order and assert the
same `expected.snapshot` each time. It is only valid when every op is stamped
(`edit` with a `stamp`, or `merge`) — an unstamped local edit takes a fresh
stamp, so its result legitimately depends on order. This is how commutativity
and idempotence are pinned without writing a case per ordering.

## Error names

Stable names, not messages. Each implementation maps them to its own errors —
Go's sentinels, and the hand-assigned codes in Rust's `error.rs` that the FFI
will carry across the C ABI at stage 8.

| name | Go |
| --- | --- |
| `bad_stamp` | `ErrBadStamp` |
| `unsupported_format` | `ErrUnsupportedFormat` |
| `not_found` | `ErrNotFound` |
| `closed` | `ErrClosed` |
| `malformed` | malformed buffer |
| `no_key` | register without a key |
| `no_value` | register has no value |
| `not_finite` | register value is not finite |
| `unstamped_register` | snapshot register is unstamped |
| `unstamped_replica` | stamped delta without a replica |

## Known properties of the wire format

Facts both implementations inherit from flatbuffers, specified here so nobody
"fixes" one side into disagreeing with the other:

- **Absence is never a default, and a default is never absence.** Flatbuffers
  omits any field equal to its default, so an encoded buffer carries no payload
  for `IntVal` 0, `FloatVal` 0.0, an empty string, an empty byte vector or
  `AssetRef.version` 0. That must stay invisible: presence lives in the **key
  space**, not in whether a scalar was written. A register that is present
  holding its type's default value, a register that is tombstoned, and a key
  that was never written are three distinct states
  (`value_at_type_default_is_present`).

  The rule this imposes on the document model: **anything whose presence
  carries meaning gets its own key, never an optional field in a table.** An
  ECS component is present because its key is present — if components were
  fields of a `Node` table, "component absent" and "component present with a
  default value" would encode identically and the distinction would be lost on
  the first round trip. Where a schema field genuinely needs presence, declare
  it optional (`field: int = null`), which generates `*int32` in Go and
  `Option<i32>` in Rust; `builder.ForceDefaults(true)` is the blunter lever.

- **-0.0 does not survive.** The same omission rule, in the one place it is
  lossy rather than invisible: `-0.0 == 0.0`, so a `FloatVal` of -0.0 is never
  written and reads back as +0.0. The loss happens when the delta is encoded,
  so no document ever holds it (`float_negative_zero_normalises`).

## Blessing

`expected.*` files are generated, not hand-written: run the Go walker with its
bless flag, read the diff, commit it. A case whose `expected.snapshot` is
missing is unblessed and must fail rather than pass vacuously.
