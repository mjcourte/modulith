# Plan: mediaprocessor

Probing is built (2026-10-12); its record is the README. Related:
[storage/PLAN.md](../storage/PLAN.md) (media metadata on versions and in the API).

## Open from probing

- mutation-check `toInfo`: `s.SideData[0]` (a landscape video should panic) and the
  display size set only when rotated (landscape should fail)
- table tests for `parseSeconds`, `atoi64`, `parseFraction` (`"N/A"`, `""`, `"0/0"`,
  `"abc"`, a normal value)
- fixtures: audio-only (with cover art), multi-audio, missing format duration
- `testdata/phone_image.json` is unused (images never reach `Probe`): delete it, or add
  a row documenting ffprobe's one-frame "video"

## Later (not scheduled)

- callable by the frontend to act on media; prefer engine or frontend playback
  manipulation over hard media edits
