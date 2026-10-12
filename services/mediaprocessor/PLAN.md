# Plan: mediaprocessor, phase 1 (probing)

Probes media files with `ffprobe` so storage can record their metadata. Decided
2026-10-12. Related: [storage/PLAN.md](../storage/PLAN.md) ("Next: media metadata":
the table, the job that calls this, images measured in storage itself).

## Decisions

- **A normal service, isolated by deployment**: a `Prober` interface, in-process in the
  monolith and over gRPC (`cmd/mediaprocessor`) when distributed, like every service.
  It has no schema and no state.
- **Isolating ffprobe**: it already runs as a child process; the risk is a crafted file
  exploiting it and reaching whatever that process can. So the child gets an empty
  environment (`cmd.Env` with only `PATH`, plus `SYSTEMROOT`/`TEMP` on Windows), never
  ours, so it can't see `GYOZA_S3_*` or `GYOZA_DATABASE_URL`; the protocol whitelist
  below stops it opening local files such as `.env`. In production it runs in its own
  container with no secrets. The monolith accepts the remaining risk (an exploit reading
  files as our user) for local dev and the MVP.
- **Input is a URL, output is a value**: storage presigns a short-lived GET and calls
  `Probe(ctx, url) (Info, error)`; mediaprocessor returns the result and storage stores
  it. mediaprocessor never calls storage (no dependency cycle, no "report back").
- **ffprobe over HTTP range requests**, never a full download:
  `ffprobe -v error -protocol_whitelist https,tls,tcp[,http for local RustFS]
  -probesize ... -analyzeduration ... -show_entries ... -of json -- <url>`, under a
  timeout. The whitelist stops crafted files from opening local files or other URLs;
  the probe limits bound how much is read. `exec.LookPath("ffprobe")` at startup.
- **Models**: `ProbeResult` (ffprobe's JSON) stays unexported; the public type is
  normalised:
  ```go
  type Info struct {
      Width, Height int // display size, after rotation; 0 = unknown
      Duration      time.Duration
      BitRate       int64 // bits/s
      Format        string
      Video         []VideoStream
      Audio         []AudioStream // a video can have several audio tracks
  }
  type VideoStream struct{ Codec string; Width, Height int; FrameRate float64; Rotation int }
  type AudioStream struct{ Codec string; SampleRate, Channels int; BitRate int64 }
  ```
  Codecs stay strings (ffprobe knows hundreds); allow-lists belong where support
  matters (the engine). `MediaAsset` goes: the ledger object and version already hold
  ID, key, size and type. Image metadata isn't probed here (storage reads headers).
- **Parsing quirks**: frame rate is a rational (`"30000/1001"`, `"0/0"` = unknown);
  durations can be `"N/A"` or missing (fall back to the stream's); rotation from stream
  side data (±90 swaps display width/height).

## Steps

1. Normalise: `ProbeResult` -> `Info` with the quirks above; table tests on captured
   ffprobe JSON (phone video with rotation, audio-only, multi-audio, missing duration).
   Done (2026-10-12): `toInfo` + `TestToInfo` (portrait with rotation, landscape with
   no side data). Deferred:
   - mutation check of `toInfo`: `s.SideData[0]` (landscape should panic) and display
     size set only when rotated (landscape should fail)
   - table tests for `parseSeconds`, `atoi64`, `parseFraction` (`"N/A"`, `""`, `"0/0"`,
     `"abc"`, a normal value)
   - fixtures: audio-only (with cover art), multi-audio, missing format duration
   - `testdata/phone_image.json`: unused (images never reach `Probe`); delete or add a
     row documenting ffprobe's one-frame "video"
2. Done (2026-10-12): `Prober`, `FFprobe` / `NewFFprobe` (finds ffprobe at startup),
   `Probe` with the flags above, the empty child environment and URLs stripped from
   errors; `ErrNoMedia` for files without audio/video. `ffprobe_test.go` probes
   `testdata/tiny_rotated.mp4` over `httptest` and checks local files are refused and
   errors hide the URL (skips without ffprobe on PATH); both security tests
   mutation-checked.
3. Public surface per the service pattern (`Prober` (done), constructor (done), `RegisterGRPC`,
   `NewGRPCClient`), `MediaProcessorService` in `contracts/proto`, `grpcapi`
   server/client; wired in-process into `cmd/gyoza` and standalone into
   `cmd/mediaprocessor` (settings, shutdown sequence).
4. Packaging: ffprobe in the monolith's image; for distributed, its own container with
   ffprobe and no S3/Postgres settings.
