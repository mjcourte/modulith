package manager

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// ErrExportNotFound means there's no export with that ID: it doesn't exist,
// was deleted, or the object isn't an export (ExportURL serves nothing else).
var ErrExportNotFound = errors.New("storage: export not found")

// Download tells the browser where to fetch a file and what to save it as.
type Download struct {
	URL       string // presigned GET; contains its own credentials
	Filename  string // what the browser saves it as
	ExpiresAt time.Time
}

// DownloadURL returns a short-lived link to one version of an asset: version
// 0 means the current one. The file is served as an attachment named after
// the asset ("Logo.png"). Trashed assets can't be downloaded; restore first.
func (m *Manager) DownloadURL(ctx context.Context, actorID, assetID string, version int) (Download, error) {
	a, err := m.store.GetAsset(ctx, assetID)
	if err != nil {
		return Download{}, err
	}
	if a.DeletedAt != nil {
		return Download{}, dam.ErrAssetNotFound
	}
	// Permission before the version lookup, so a user without access can't
	// probe which versions exist.
	if err := m.canRead(ctx, actorID, a.Owner, a.ProjectID); err != nil {
		return Download{}, err
	}

	if version == 0 {
		version = a.CurrentVersion
	}
	v, err := m.store.GetVersion(ctx, assetID, version)
	if err != nil {
		return Download{}, err
	}

	obj, err := m.ledger.Get(ctx, v.ObjectID)
	if err != nil || obj.DeletedAt != nil {
		// The asset and the ledger disagree; don't hand out a link to nothing.
		return Download{}, fmt.Errorf("%w: version %d's file is missing from the ledger", dam.ErrAssetNotFound, version)
	}
	return m.presignDownload(ctx, obj, downloadFilename(a.Name, v.ContentType))
}

// ExportURL returns a short-lived link to an export (a rendered output
// recorded in the ledger with Kind=export, e.g. by the documents service).
func (m *Manager) ExportURL(ctx context.Context, actorID, objectID string) (Download, error) {
	obj, err := m.ledger.Get(ctx, objectID)
	if errors.Is(err, ledger.ErrNotFound) {
		return Download{}, ErrExportNotFound
	}
	if err != nil {
		return Download{}, fmt.Errorf("look up export: %w", err)
	}
	if obj.Kind != ledger.KindExport || obj.DeletedAt != nil {
		return Download{}, ErrExportNotFound
	}
	if err := m.canRead(ctx, actorID, obj.Owner, obj.ProjectID); err != nil {
		return Download{}, err
	}
	name := "export-" + obj.CreatedAt.Format("2006-01-02") // Go's layout for YYYY-MM-DD
	return m.presignDownload(ctx, obj, downloadFilename(name, obj.ContentType))
}

func (m *Manager) presignDownload(ctx context.Context, obj ledger.Object, filename string) (Download, error) {
	url, err := m.blobs.PresignGet(ctx, obj.Bucket, obj.Key, m.cfg.DownloadURLTTL, filename)
	if err != nil {
		return Download{}, fmt.Errorf("presign download: %w", err)
	}
	return Download{URL: url, Filename: filename, ExpiresAt: time.Now().Add(m.cfg.DownloadURLTTL)}, nil
}

// extensions are the file extensions downloads get, by MIME type. A fixed
// table rather than mime.ExtensionsByType, which on Windows reads the system
// registry, so the same file would get different names on different machines.
var extensions = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"image/svg+xml": ".svg", "image/avif": ".avif", "image/heic": ".heic",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
	"audio/mpeg": ".mp3", "audio/wav": ".wav", "audio/ogg": ".ogg", "audio/aac": ".aac",
	"font/woff2": ".woff2", "font/woff": ".woff", "font/ttf": ".ttf", "font/otf": ".otf",
	"application/pdf": ".pdf", "application/json": ".json", "application/zip": ".zip",
	"text/plain": ".txt", "text/csv": ".csv", "text/markdown": ".md",
}

// maxFilenameRunes leaves room for the extension under the common 255-byte
// filename limit, even with multi-byte characters.
const maxFilenameRunes = 120

// downloadFilename makes a file name that saves cleanly on every OS: the
// name made portable (the folder-name rules: no <>:"/\|?*, control
// characters, trailing dots or reserved device names), shortened if needed,
// with the extension for contentType added unless the name already has it.
func downloadFilename(name, contentType string) string {
	ext := ""
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		ext = extensions[mediaType]
	}

	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(dam.ForbiddenNameChars, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	clean := b.String()
	// "Logo.PNG" already ends in the right extension: don't add another.
	if ext != "" && strings.EqualFold(path.Ext(clean), ext) {
		clean = strings.TrimSuffix(clean, path.Ext(clean))
	}
	if utf8.RuneCountInString(clean) > maxFilenameRunes {
		clean = string([]rune(clean)[:maxFilenameRunes])
	}
	clean = strings.TrimRight(clean, ". ")
	if dam.IsReservedName(clean) {
		clean = "_" + clean // "CON" can't be a file name on Windows
	}
	if clean == "" {
		clean = "download"
	}
	return clean + ext
}
