package ledger

import (
	"encoding/json"
	"fmt"
	"time"
)

// MediaStatus says where an object's media metadata stands.
type MediaStatus string

const (
	MediaPending MediaStatus = "pending" // queued for probing
	MediaReady   MediaStatus = "ready"   // probed; the fields are filled in
	MediaFailed  MediaStatus = "failed"  // probing gave up; Error says why
)

// MediaInfo is the media metadata recorded for one object, so its
// dimensions and duration can be read without fetching the file. Zero
// values mean unknown.
type MediaInfo struct {
	ObjectID string
	Status   MediaStatus
	Width    int // display size, after rotation
	Height   int
	Duration time.Duration // kept to the microsecond
	// Details is the full probe result as a JSON object; the ledger stores
	// it without interpreting it. It comes back as equivalent JSON, not
	// byte for byte (Postgres normalises it). Empty means {}.
	Details   json.RawMessage
	Error     string // why the last attempt failed
	Attempts  int    // failed attempts so far
	UpdatedAt time.Time
}

// normalize validates m for SetMediaInfo and brings it to what every
// implementation stores. Shared by every implementation.
func (m MediaInfo) normalize() (MediaInfo, error) {
	switch m.Status {
	case MediaPending, MediaReady, MediaFailed:
	default:
		return m, fmt.Errorf("%w: unknown media status %q", ErrInvalidObject, m.Status)
	}
	if len(m.Details) == 0 {
		m.Details = json.RawMessage("{}")
	}
	if !json.Valid(m.Details) {
		return m, fmt.Errorf("%w: media details aren't valid JSON", ErrInvalidObject)
	}
	m.Duration = m.Duration.Truncate(time.Microsecond)
	return m, nil
}
