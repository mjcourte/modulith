package storagemanager

import (
	"errors"
	"testing"
)

// Real file headers (magic numbers) for the formats being checked.
var (
	pngBytes   = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegBytes  = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifBytes   = []byte("GIF89a\x01\x00\x01\x00")
	pdfBytes   = []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	zipBytes   = []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00")
	mp4Bytes   = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	mp3Bytes   = []byte("ID3\x04\x00\x00\x00\x00\x00\x00")
	oggBytes   = []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00")
	woff2Bytes = []byte("wOF2\x00\x01\x00\x00")
	jsonBytes  = []byte(`{"name": "logo", "size": 2}`)
	svgBytes   = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10"></svg>`)
	xmlSVG     = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	htmlBytes  = []byte("<!DOCTYPE html><html><body>hi</body></html>")
	textBytes  = []byte("just some notes\nwith lines\n")
	opaque     = []byte{0x00, 0x13, 0x37, 0xfe, 0x01, 0x02, 0xc0, 0xde} // nothing sniffable
)

func TestCheckSniffed(t *testing.T) {
	allowed := []struct {
		declared string
		bytes    []byte
	}{
		{"image/png", pngBytes},
		{"image/png", jpegBytes}, // wrong subtype, same category: still an image
		{"image/gif", gifBytes},
		{"application/pdf", pdfBytes},
		{"video/mp4", mp4Bytes},
		{"audio/mpeg", mp3Bytes},
		{"audio/ogg", oggBytes},
		{"video/ogg", oggBytes},
		{"font/woff2", woff2Bytes},
		{"application/json", jsonBytes},
		{"application/json; charset=utf-8", jsonBytes},
		{"image/svg+xml", svgBytes},
		{"image/svg+xml", xmlSVG},
		{"text/plain", textBytes},
		{"text/markdown", textBytes},
		{"image/heic", opaque}, // not sniffable: allowed
		{"application/zip", zipBytes},
		{"application/x-anything", pngBytes}, // declared "other": nothing to compare
	}
	for _, tt := range allowed {
		if err := checkSniffed(tt.declared, tt.bytes); err != nil {
			t.Errorf("checkSniffed(%s, %.12q) = %v, want allowed", tt.declared, tt.bytes, err)
		}
	}

	rejected := []struct {
		declared string
		bytes    []byte
	}{
		{"image/png", zipBytes},
		{"image/png", htmlBytes},
		{"image/png", textBytes},
		{"image/png", pdfBytes},
		{"video/mp4", pngBytes},
		{"application/pdf", jpegBytes},
		{"application/json", pngBytes},
		{"font/woff2", htmlBytes},
		{"image/png", []byte{}}, // an empty "image" sniffs as text
	}
	for _, tt := range rejected {
		if err := checkSniffed(tt.declared, tt.bytes); !errors.Is(err, ErrContentMismatch) {
			t.Errorf("checkSniffed(%s, %.12q) = %v, want ErrContentMismatch", tt.declared, tt.bytes, err)
		}
	}
}
