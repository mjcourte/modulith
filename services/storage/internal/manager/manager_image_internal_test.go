package manager

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

// encoded returns a 30x20 image encoded by enc.
func encoded(t testing.TB, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewRGBA(image.Rect(0, 0, 30, 20))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func pngOf(t testing.TB) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
}

func jpegOf(t testing.TB) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
}

// withExif returns a 30x20 JPEG carrying an EXIF segment with the given
// orientation, written in order (binary.LittleEndian is "II", BigEndian "MM").
func withExif(t testing.TB, order binary.ByteOrder, orientation uint16) []byte {
	t.Helper()
	// TIFF: header, then IFD0 at offset 8 with one entry and no next IFD.
	tiff := make([]byte, 8+2+12+4)
	if order == binary.ByteOrder(binary.LittleEndian) {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)       // one entry
	order.PutUint16(tiff[10:], 0x0112) // Orientation
	order.PutUint16(tiff[12:], 3)      // SHORT
	order.PutUint32(tiff[14:], 1)      // one value
	order.PutUint16(tiff[18:], orientation)
	return spliceAPP1(jpegOf(t), append([]byte("Exif\x00\x00"), tiff...))
}

// spliceAPP1 inserts an APP1 segment with payload right after a JPEG's
// start-of-image marker.
func spliceAPP1(jpg, payload []byte) []byte {
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{0xFF, 0xD8}, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func TestMeasureImage(t *testing.T) {
	webp, err := os.ReadFile("testdata/30x20.webp") // made with ffmpeg; x/image has no WebP encoder
	if err != nil {
		t.Fatal(err)
	}
	gifData := encoded(t, func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) })

	tests := []struct {
		name          string
		data          []byte
		width, height int
		details       imageDetails
	}{
		{"png", pngOf(t), 30, 20, imageDetails{Format: "png"}},
		{"gif", gifData, 30, 20, imageDetails{Format: "gif"}},
		{"webp", webp, 30, 20, imageDetails{Format: "webp"}},
		{"jpeg without exif", jpegOf(t), 30, 20, imageDetails{Format: "jpeg", Orientation: 1}},
		{"jpeg upright, II", withExif(t, binary.LittleEndian, 1), 30, 20, imageDetails{Format: "jpeg", Orientation: 1}},
		{"jpeg mirrored, not rotated", withExif(t, binary.BigEndian, 3), 30, 20, imageDetails{Format: "jpeg", Orientation: 3}},
		// A portrait phone photo: stored 30x20, shown 20x30.
		{"jpeg rotated 90, II", withExif(t, binary.LittleEndian, 6), 20, 30, imageDetails{Format: "jpeg", Orientation: 6}},
		{"jpeg rotated 90, MM", withExif(t, binary.BigEndian, 6), 20, 30, imageDetails{Format: "jpeg", Orientation: 6}},
		{"jpeg rotated 270", withExif(t, binary.BigEndian, 8), 20, 30, imageDetails{Format: "jpeg", Orientation: 8}},
		{"jpeg invalid orientation", withExif(t, binary.BigEndian, 9), 30, 20, imageDetails{Format: "jpeg", Orientation: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := measureImage(tc.data)
			if err != nil {
				t.Fatalf("measureImage: %v", err)
			}
			want := imageInfo{Width: tc.width, Height: tc.height, Details: tc.details}
			if got != want {
				t.Errorf("measureImage = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMeasureImageRejects(t *testing.T) {
	// HEIC: an ISO-BMFF "ftyp" box, which no Go decoder claims.
	heic := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")
	big := pngOf(t)
	// A JPEG whose size comes after more than imageHeaderLen of other segments.
	padded := jpegOf(t)
	for len(padded) <= imageHeaderLen {
		padded = spliceAPP1(padded, make([]byte, 60000)) // not EXIF: skipped
	}

	tests := map[string][]byte{
		"heic":                heic,
		"junk":                []byte("not an image at all"),
		"truncated":           big[:20],
		"empty":               nil,
		"cut-off long header": padded[:imageHeaderLen],
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := measureImage(data); err == nil {
				t.Errorf("measureImage = %+v, want an error", got)
			}
		})
	}
}

// TestJPEGOrientationMalformed: EXIF comes from strangers. Broken offsets and
// lengths must give 1, never a panic or a wrong value.
func TestJPEGOrientationMalformed(t *testing.T) {
	good := withExif(t, binary.BigEndian, 6)
	app1 := 2 + 4 + 6 // offset of the TIFF data: SOI, segment header, "Exif\0\0"
	corrupt := func(at int, b ...byte) []byte {
		out := bytes.Clone(good)
		copy(out[at:], b)
		return out
	}
	tests := map[string][]byte{
		"no SOI":                  good[2:],
		"segment length past end": corrupt(4, 0xFF, 0xFF),
		"segment length below 2":  corrupt(4, 0, 1),
		"bad byte order":          corrupt(app1, 'X', 'X'),
		"not 42":                  corrupt(app1+2, 0, 43),
		"IFD offset past end":     corrupt(app1+4, 0x7F, 0xFF, 0xFF, 0xFF),
		"IFD offset wraps":        corrupt(app1+4, 0xFF, 0xFF, 0xFF, 0xFF),
		// Count says 65535 entries; the only real one isn't orientation.
		"entries run past end": corrupt(app1+8, 0xFF, 0xFF, 0x01, 0x10),
		"cut inside the entry": good[:app1+8+2+6],
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if got := jpegOrientation(data); got != 1 {
				t.Errorf("jpegOrientation = %d, want 1", got)
			}
		})
	}
	if got := jpegOrientation(good); got != 6 {
		t.Errorf("jpegOrientation(good) = %d, want 6 (the corruptions above must be what breaks it)", got)
	}
}

// FuzzJPEGOrientation feeds arbitrary bytes to the EXIF parser. Its seeds run
// in every go test; explore with
//
//	go test ./services/storage/internal/manager -run '^$' -fuzz FuzzJPEGOrientation -fuzztime 30s
func FuzzJPEGOrientation(f *testing.F) {
	f.Add(jpegOf(f))
	f.Add(withExif(f, binary.LittleEndian, 6))
	f.Add(withExif(f, binary.BigEndian, 8))
	f.Fuzz(func(t *testing.T, data []byte) {
		if o := jpegOrientation(data); o < 1 || o > 8 {
			t.Errorf("jpegOrientation = %d, want 1-8", o)
		}
	})
}
