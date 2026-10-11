package mediaprocessor

import "time"

// TODO this is a draft
// based on what is/will be recorded in ledger, some of this might be redundant?
// or we might need some keys to connect eg assetID
type MediaAsset struct {
	ID          string
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	CreatedAt   time.Time
}

// how will we ledger this...

type ImageMetadata struct {
	Format     string // possible enum
	Width      int
	Height     int
	ColorSpace string
	BitDepth   int
	HasAlpha   *bool
}

type VideoMetadata struct {
	Duration   time.Duration
	FrameRate  float32
	Width      int
	Height     int
	VideoCodec string // possible enum instead
}

type AudioMetadata struct {
	Duration   time.Duration
	SampleRate float32
	Channels   int    // what is non-specific int in Go?
	AudioCodec string // possible enum supported
	BitRate    float32
}
