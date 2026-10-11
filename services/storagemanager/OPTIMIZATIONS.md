# Storage Manager: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

## Done

None yet.

## Open

### MemoryBlobs never forgets grants
- **Expected:** every presign adds an entry to `grants` and nothing removes it,
  so a long-running process using the fake grows without bound.
- **When to act:** only if the fake is ever used outside tests and short local runs.
- **Fix:** drop expired grants when `lookupGrant` comes across them, or sweep periodically.
