package manager

import (
	"errors"
	"fmt"
	"gyoza/services/storage/internal/dam"
	"net/http"
	"strings"
)

// ErrContentMismatch means a file's bytes clearly aren't what its declared
// type says, e.g. a zip uploaded as image/png.
var ErrContentMismatch = errors.New("storage: content doesn't match declared type")

// SniffLen is how many leading bytes sniffing looks at: all that
// http.DetectContentType reads.
const SniffLen = 512

// checkSniffed compares a file's first bytes against its declared MIME type.
//
// http.DetectContentType recognises common formats by their magic numbers
// (PNG, JPEG, GIF, WebP, PDF, zip, MP4, WebM, MP3, fonts, ...) and otherwise
// says "text/plain" or "application/octet-stream". It can't tell everything
// apart, so this only rejects clear mismatches:
//   - unknown bytes (octet-stream) are allowed: plenty of valid formats aren't sniffable
//   - the same category is allowed: a JPEG declared as image/png is still an image
//   - text is allowed for text-based types: JSON and SVG sniff as plain text or XML
//   - Ogg is allowed for audio or video: the container holds either
//   - anything declared "other" is allowed: there's nothing to compare against
//
// Everything else, e.g. zip or HTML bytes declared as an image, is rejected.
func checkSniffed(declared string, prefix []byte) error {
	detected := http.DetectContentType(prefix)
	want, got := dam.CategoryOf(declared), dam.CategoryOf(detected)

	switch {
	case strings.HasPrefix(detected, "application/octet-stream"):
		return nil
	case want == dam.CategoryOther || want == got:
		return nil
	case got == dam.CategoryText && (want == dam.CategoryJSON || isSVG(declared)):
		return nil
	case strings.HasPrefix(detected, "application/ogg") && (want == dam.CategoryAudio || want == dam.CategoryVideo):
		return nil
	}
	return fmt.Errorf("%w: declared %s, but the bytes look like %s", ErrContentMismatch, declared, detected)
}

// isSVG reports whether a MIME type is SVG, the one text-based image format.
func isSVG(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "image/svg+xml")
}
