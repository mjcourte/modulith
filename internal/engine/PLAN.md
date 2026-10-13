# Plan: engine (`internal/engine`, `engine/`)

Clocks, peers and last-writer-wins belong to the engine: the mock defines the contract
the native engine must keep, and documents and the gateway only rely on it. Several
decisions below land in [documents](../../services/documents/README.md) (replica IDs,
how a refused delta is handled) or the gateway (server time, per-socket replica IDs).

## Clocks and LWW: failure modes (draft, 2026-10-12)

How stamps work today (README "Status"): every register is last-writer-wins on a stamp
`(HLC, replica)`. A local edit ticks `max(clock+1, HLC(now))`; a received stamp raises
the clock; equal HLCs go to the higher replica ID; an equal stamp is "the same write",
so neither wins. Servers refuse stamps past `now + MaxSkew` (1 minute), in deltas and in
snapshots. Deletes are tombstones, kept forever. A revert is one fresh edit.

Behaviour is read from `mock.go`; "test" names an existing test, or *to write*.

| # | Scenario | What happens today | Test | Proposed decision |
| --- | --- | --- | --- | --- |
| 1 | **Two writers share a replica ID** (two tabs, a reused ID) and write different values to one key in the same tick | **Fixed (2026-10-12):** equal stamps are broken by the value (its type, then its JSON), so every replica keeps the same one. Before, each kept whichever arrived first and diverged permanently | `TestMockConvergesWhenReplicaIDsCollide` | Replica IDs must still be unique per writer (the gateway assigns one per socket; the server's is per process): with a shared ID, one write silently replaces the other |
| 2 | **Server replicas' clocks differ by more than MaxSkew** | **Fixed (2026-10-12):** snapshots aren't bounded, and a delta with `DeltaInput.Trusted` isn't either; documents trusts stream deltas with `Gyoza-Origin` (published by a documents replica). Before, the slower server terminated the faster one's deltas and couldn't load its snapshots | `TestFastServerClockIsTrusted` in documents' manager, `TestMockRejectsFutureStamps` | The gateway must never set `Gyoza-Origin`: browsers' stamps are bounded because it's empty (`TestForgedStampsAreRefused`). To do: alert on server clock skew |
| 3 | **A browser's clock is ahead by more than MaxSkew** | Its edits are refused by the server | `TestMockRejectsFutureStamps` | Gateway sends server time on connect (planned); the WASM engine stamps with the offset. Decide what the browser does with a refused edit: resync and retry, not silently drop |
| 4 | **A browser's clock is behind** (no or stale offset) | Its edits stamp `max(clock+1, HLC(now))`; once it has received anything, its clock is raised, so it still wins over what it has seen. Concurrent edits against faster clocks lose | `TestMockIntermittentPeerIsNotLeftBehind` covers the offline half | Accept: last-writer-wins by (roughly) wall time is the contract. The server-time offset narrows it |
| 5 | **An authorized user forges stamps up to now + MaxSkew** | Their writes win every conflict for up to a minute ahead, and raise everyone's clock by as much | `TestForgedStampsAreRefused` (only beyond the bound) | Accept for now (they can write anyway); lower MaxSkew once server-time sync exists. The gateway refuses another replica's ID (planned) |
| 6 | **Offline edits arrive late** | Stamped at their real time, so they lose to anything written after them, silently | `TestMockIntermittentPeerIsNotLeftBehind` | Accept as LWW, but the frontend should show "N of your offline changes were overwritten" (the engine can report keys where an applied delta lost) |
| 7 | **One edit sets several keys; a concurrent edit sets some of them** | Each key is decided alone: the result can mix both edits (a half-moved group) | *to write* | Inherent to per-register LWW. The real engine's model decides: keep related state in one register (a transform is one value), or make groups atomic |
| 8 | **Delete vs concurrent edit** | Whichever stamp is higher wins; a delete can win over an edit made "after" on a slower clock | *to write* | Accept (LWW). Note it for the real engine: graph CRDTs (Yrs etc.) usually let edits beat concurrent deletes |
| 9 | **Tombstones are never collected** | No resurrection, but snapshots grow with every deleted key | — | Defer: collect tombstones older than the stream's MaxAge (7d) at snapshot time, since no live delta can be older. Needs offline peers limited to 7d (else full resync). OPTIMIZATIONS.md when it matters |
| 10 | **Revert vs an edit stamped after it but made before it** (a slow peer that hadn't seen the revert) | The later stamp wins: part of the revert is undone | *to write* | Accept: the revert is an ordinary edit; re-reverting is possible. Admin docs should say so |
| 11 | **A server restarts, or its clock jumps backwards** | A new process is a new replica (fresh ID); a loaded doc's clock is raised to its snapshot's and replayed deltas' highest stamps, so it never stamps below what it holds | — (by construction) | Accept; add a test that a reloaded doc ticks above every stored stamp |
| 12 | **HLC counter overflow** (65,536 ticks within one millisecond) | The counter carries into the millisecond bits: stamps run slightly ahead of wall time | — | Accept: harmless, and unreachable at human edit rates |

### Questions to settle before the native engine

- Is the document a map of registers (the mock) or a graph/tree CRDT (Yrs)? Rows 7 and
  8 change completely with a tree: moves, parent deletes, and orphaned children become
  the hard cases.
- Does the native engine keep HLC stamps, or a CRDT library's own clock? If its own,
  rows 2-5 (skew checks, server time) need re-deciding against it.
- How does the engine report "your write lost" (row 6) to the frontend?
- Decide winners by stream order instead of client stamps? Every edit passes through the
  `DOCUMENTS` stream, whose sequence numbers are a total order nobody can forge: the
  edit that reaches the stream last wins. That removes forged stamps and clock skew
  (rows 2-5) entirely. The cost: a browser can't know whether its optimistic edit wins
  until the stream answers, and offline edits always win on reconnect because they
  arrive last (row 6 inverted). Hybrid: stream order between servers, stamps only for
  ordering a browser's own pending edits.
