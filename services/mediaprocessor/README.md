# Media Processor

- basically wrapping ffmpeg, lets be honest
- probes uploaded video/audio with `ffprobe` over a presigned URL (`Probe`, built; storage runs it, see storage README "Media metadata"); plan in [PLAN.md](PLAN.md)
- callable by storage service to fill media details at upload-time
- DEFERRED callable by FE to act on media assets (prefer engine or FE maniplation of playback rather hard media edits though)
- look at `ffprobe`
