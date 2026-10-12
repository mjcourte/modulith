package manager

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDownloadFilename(t *testing.T) {
	tests := []struct{ name, contentType, want string }{
		{"Logo", "image/png", "Logo.png"},
		{"Logo.png", "image/png", "Logo.png"},     // extension already there
		{"Logo.PNG", "image/png", "Logo.png"},     // in another case: normalised
		{"Logo.jpg", "image/png", "Logo.jpg.png"}, // the real type wins
		{"Brand: v2/final?", "image/png", "Brand_ v2_final_.png"},
		{"  spaced  ", "image/png", "spaced.png"},
		{"trailing.", "application/pdf", "trailing.pdf"},
		{"CON", "text/plain", "_CON.txt"}, // reserved on Windows
		{"nul.txt", "text/plain", "_nul.txt"},
		{"Ärger & Co", "application/json; charset=utf-8", "Ärger & Co.json"},
		{"", "image/png", "download.png"},
		{"...", "image/png", "download.png"},
		{"notes", "application/x-unknown", "notes"}, // unknown type: no extension
		{"tab\there", "image/png", "tab_here.png"},
	}
	for _, tt := range tests {
		if got := downloadFilename(tt.name, tt.contentType); got != tt.want {
			t.Errorf("downloadFilename(%q, %q) = %q, want %q", tt.name, tt.contentType, got, tt.want)
		}
	}

	long := downloadFilename(strings.Repeat("ñ", 300), "image/png")
	if n := utf8.RuneCountInString(long); n != maxFilenameRunes+len(".png") || !strings.HasSuffix(long, ".png") {
		t.Errorf("long name became %d characters (%q...), want %d ending in .png", n, long[:10], maxFilenameRunes+4)
	}
	if len(long) > 255 {
		t.Errorf("long name is %d bytes, over the common 255-byte limit", len(long))
	}
}
