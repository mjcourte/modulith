# NATS: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

## Rules the design depends on

- Consumers are per service instance, never per project, client or request
  (README "Rule: few consumers"). The previous version of Modulith scaled to
  about 3N consumers per active project. Per-project reads use Direct Get.
- `Compact` runs after every stored snapshot, so a project's deltas in the
  stream are only the ones since its last snapshot. Replays depend on that tail
  staying short.

## Done

#### One stream instead of a consumer per open project

- **Found:** the earlier design had the gateway open an ordered consumer per
  open project. That repeats the consumer-per-project scaling the previous
  version ran into.
- **Fix (2026-10-12):** one `DOCUMENTS` stream with `AllowDirect`; one consumer
  per service instance with fan-out after it; `LatestSnapshot` and `DeltasAfter`
  read by Direct Get.

## Open

#### `DeltasAfter` makes one Direct Get per delta

- **Now:** nats.go v1.54 has no batch Direct Get helper, so replaying `n` deltas
  is `n` sequential round trips (`GetMsg` with next-by-subject). It's run once per
  project load, not per edit.
- **Why it's fine for now:** with debounced snapshots and `Compact`, the tail is
  a few seconds of edits. At under 1ms per round trip on a local network, 50
  deltas is a few tens of ms per load. Not measured yet: documents doesn't exist.
- **Fix:** batch Direct Get (NATS 2.11+; compose runs 2.14). Send one request to
  `$JS.API.DIRECT.GET.DOCUMENTS` with `{"seq": n, "next_by_subj": subj, "batch": N}`;
  the server streams up to N messages, then an end-of-batch status. First check
  Synadia's `orbit.go` (`jetstreamext`) for a maintained Go helper (unverified)
  before porting it from nats.js (about 60-80 lines: an inbox subscription, header
  parsing, the end marker, timeouts). Only the inside of `DeltasAfter` changes.
- **When to act:** documents records deltas replayed and milliseconds per load,
  and either is high: loads are frequent (a short TTL with many projects), tails
  are long (snapshots failing or slow), or NATS is across a slower network than
  the services (another AZ).
