package manager_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// imageBytes returns a 30x20 image encoded by enc.
func imageBytes(t *testing.T, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewRGBA(image.Rect(0, 0, 30, 20))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// jpegWithPadding returns a 30x20 JPEG whose size marker sits after pad bytes
// of other APP segments, as phone photos' EXIF/XMP/ICC push it back.
func jpegWithPadding(t *testing.T, pad int) []byte {
	t.Helper()
	jpg := imageBytes(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
	out := []byte{0xFF, 0xD8}
	for pad > 0 {
		n := min(pad, 60000)
		seg := []byte{0xFF, 0xE2, 0, 0} // APP2, not EXIF: skipped by every reader
		binary.BigEndian.PutUint16(seg[2:], uint16(n+2))
		out = append(append(out, seg...), make([]byte, n)...)
		pad -= n
	}
	return append(out, jpg[2:]...)
}

func TestCompleteUploadMeasuresImages(t *testing.T) {
	pngData := imageBytes(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
	tests := []struct {
		name, contentType string
		data              []byte
		format            string
	}{
		{"png", "image/png", pngData, "png"},
		// The size is ~100 KiB in: found only because images read more than
		// the 512 bytes sniffing needs.
		{"jpeg with large metadata", "image/jpeg", jpegWithPadding(t, 100<<10), "jpeg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tm := newTestManager(t)
			objectID, _ := uploadData(t, tm, tc.contentType, tc.data)

			info, err := tm.ledger.GetMediaInfo(t.Context(), objectID)
			if err != nil {
				t.Fatalf("GetMediaInfo: %v", err)
			}
			if info.Status != ledger.MediaReady || info.Width != 30 || info.Height != 20 {
				t.Errorf("media info = %+v; want ready, 30x20", info)
			}
			var details struct{ Format string }
			if err := json.Unmarshal(info.Details, &details); err != nil || details.Format != tc.format {
				t.Errorf("Details = %s (%v); want format %q", info.Details, err, tc.format)
			}
		})
	}
}

// TestCompleteUploadUnmeasurableImage: a format Go can't read (HEIC from an
// iPhone) is still a valid upload; only its media info says failed.
func TestCompleteUploadUnmeasurableImage(t *testing.T) {
	tm := newTestManager(t)
	// Binary bytes no decoder claims; they sniff as octet-stream, which the
	// content check allows for any declared type.
	heic := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 256)
	objectID, a := uploadData(t, tm, "image/heic", heic)

	if a.Category != dam.CategoryImage {
		t.Errorf("Category = %q, want image", a.Category)
	}
	info, err := tm.ledger.GetMediaInfo(t.Context(), objectID)
	if err != nil || info.Status != ledger.MediaFailed || info.Error == "" {
		t.Errorf("media info = %+v, %v; want failed with a reason", info, err)
	}
}

func TestCompleteUploadVector(t *testing.T) {
	tm := newTestManager(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 30 20"></svg>`)
	objectID, a := uploadData(t, tm, "image/svg+xml", svg)

	if a.Category != dam.CategoryVector {
		t.Errorf("Category = %q, want vector", a.Category)
	}
	if _, err := tm.ledger.GetMediaInfo(t.Context(), objectID); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("GetMediaInfo = %v; want none (no pixel size to record)", err)
	}
}
