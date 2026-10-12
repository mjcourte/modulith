package mediaprocessor

import "time"

// VideoStream is one video track. Width and Height are as stored, before
// Rotation is applied.
type VideoStream struct {
	FrameRate float64 `json:"frameRate"` // average frames per second (ffprobe's avg_frame_rate)
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Rotation  int     `json:"rotation"` // degrees as ffprobe reports them, e.g. -90 for a portrait phone video
	Codec     string  `json:"codec"`
	BitRate   int64   `json:"bitRate"` // bits per second
}

// AudioStream is one audio track.
type AudioStream struct {
	SampleRate int    `json:"sampleRate"` // Hz
	Channels   int    `json:"channels"`
	Codec      string `json:"codec"`
	BitRate    int64  `json:"bitRate"` // bits per second
}

// Info is what probing a media file found. Zero values mean unknown; an empty
// Video or Audio means the file has no streams of that kind. Storage keeps it
// as JSON (media info details), so the tags are a stored format: rename with care.
type Info struct {
	Duration      time.Duration `json:"durationNs"`   // nanoseconds in JSON, as Duration marshals
	DisplayWidth  int           `json:"displayWidth"` // size as viewed, after rotation
	DisplayHeight int           `json:"displayHeight"`
	BitRate       int64         `json:"bitRate"` // bits per second, whole file
	Format        string        `json:"format"`  // ffprobe's format_name, a comma-separated list, e.g. "mov,mp4,m4a,3gp,3g2,mj2"
	Video         []VideoStream `json:"video"`
	Audio         []AudioStream `json:"audio"`
}
