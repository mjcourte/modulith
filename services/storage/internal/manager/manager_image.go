package manager

// Measuring images: a raster image's size is in its header, so CompleteUpload
// reads it from the file's first bytes (one range request) instead of
// queuing a probe job as for video. Vector images (SVG) have no pixel size
// and aren't measured.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"

	// Decoders register themselves with package image when imported; the
	// blank imports are only for that side effect.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// imageHeaderLen is how much of an image is read to measure it. JPEG puts
// EXIF, XMP and ICC segments (up to 64 KiB each) before the size.
const imageHeaderLen = 256 << 10

// imageInfo is what measureImage found. Width and Height are the display
// size, after EXIF orientation.
type imageInfo struct {
	Width   int
	Height  int
	Details imageDetails
}

// imageDetails is stored as the media info's JSON details.
type imageDetails struct {
	Format      string `json:"format"`                // "jpeg", "png", "gif" or "webp"
	Orientation int    `json:"orientation,omitempty"` // EXIF orientation, JPEG only; 0 = none
}

// measureImage reads a raster image's size from the start of its file.
// It fails for formats without a decoder (HEIC, AVIF, BMP, ...), for headers
// longer than prefix, and for corrupt headers.
func measureImage(prefix []byte) (imageInfo, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(prefix))
	switch {
	case errors.Is(err, image.ErrFormat):
		return imageInfo{}, errors.New("unsupported image format")
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		if len(prefix) < imageHeaderLen { // prefix is the whole file
			return imageInfo{}, errors.New("image file is truncated")
		}
		return imageInfo{}, fmt.Errorf("image header not within the first %d KiB", imageHeaderLen>>10)
	case err != nil:
		return imageInfo{}, fmt.Errorf("read image header: %w", err)
	}

	info := imageInfo{Width: config.Width, Height: config.Height, Details: imageDetails{Format: format}}
	if format == "jpeg" {
		o := jpegOrientation(prefix)
		info.Details.Orientation = o
		if o >= 5 && o <= 8 { // rotated 90 degrees: shown the other way round
			info.Width, info.Height = info.Height, info.Width
		}
	}
	return info, nil
}

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 1 if it has
// none. It parses untrusted bytes, so every offset is checked: anything
// malformed also gives 1, never a panic.
func jpegOrientation(b []byte) int {
	tiff, ok := jpegExif(b)
	if !ok {
		return 1
	}
	// TIFF header: byte order, 42, offset of the first directory (IFD0).
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("II")):
		order = binary.LittleEndian
	case bytes.HasPrefix(tiff, []byte("MM")):
		order = binary.BigEndian
	default:
		return 1
	}
	if len(tiff) < 8 || order.Uint16(tiff[2:]) != 42 {
		return 1
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) { // also catches a huge offset that wrapped negative
		return 1
	}
	// IFD0: a count, then 12-byte entries: tag, type, count, value.
	n := int(order.Uint16(tiff[ifd:]))
	for i := range n {
		e := ifd + 2 + 12*i
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:]) == 0x0112 { // Orientation, a SHORT in the value's first 2 bytes
			if o := int(order.Uint16(tiff[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// jpegExif returns the TIFF data of a JPEG's EXIF segment (APP1 starting with
// "Exif\0\0"), walking the segments that precede the image data.
func jpegExif(b []byte) ([]byte, bool) {
	if !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) { // start of image
		return nil, false
	}
	pos := 2
	for pos+4 <= len(b) {
		if b[pos] != 0xFF {
			return nil, false
		}
		marker := b[pos+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan or end of image: no EXIF before the data
			return nil, false
		}
		// The length counts its own 2 bytes but not the FF xx marker.
		length := int(binary.BigEndian.Uint16(b[pos+2:]))
		if length < 2 || pos+2+length > len(b) {
			return nil, false
		}
		payload := b[pos+4 : pos+2+length]
		if marker == 0xE1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			return payload[6:], true
		}
		pos += 2 + length // always moves forward, so the loop ends
	}
	return nil, false
}

// recordImageInfo measures an uploaded image from prefix and records the
// result for its ledger object: ready with its size, or failed with why.
// A file that can't be measured is still a valid upload, so only failing to
// record is an error.
func (m *Manager) recordImageInfo(ctx context.Context, objectID string, prefix []byte) error {
	record := ledger.MediaInfo{ObjectID: objectID}
	if info, measureErr := measureImage(prefix); measureErr != nil {
		record.Status = ledger.MediaFailed
		record.Error = measureErr.Error()
	} else {
		details, err := json.Marshal(info.Details)
		if err != nil {
			return err
		}
		record.Status = ledger.MediaReady
		record.Width, record.Height = info.Width, info.Height
		record.Details = details
	}
	if err := m.ledger.SetMediaInfo(ctx, record); err != nil {
		return fmt.Errorf("record image info: %w", err)
	}
	return nil
}
