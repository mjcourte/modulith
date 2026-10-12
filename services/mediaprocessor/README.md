# Media Processor

- basically wrapping ffmpeg, lets be honest
- todo: probe uploaded video/audio with `ffprobe` (over a presigned URL, range requests, no full download) and report metadata back to storage for the version; see storage README "Todo: media metadata"
- callable by storage service to fill media details at upload-time
- DEFERRED callable by FE to act on media assets (prefer engine or FE maniplation of playback rather hard media edits though)
- look at `ffprobe`
