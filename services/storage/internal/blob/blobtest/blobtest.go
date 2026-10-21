// Package blobtest is the shared test suite for blob.Store implementations.
// It lives in its own package, like net/http/httptest, so the testing
// package is never compiled into binaries that only use blob.
package blobtest

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
)

// Harness is one blob.Store implementation plus a way to act as the browser
// against its presigned URLs.
type Harness struct {
	Store  blob.Store
	Bucket string                                           // must already exist (matters for S3)
	Put    func(url, contentType string, data []byte) error // browser upload
	Get    func(url string) ([]byte, string, error)         // browser download: bytes and saved-as filename; blob.ErrNotFound when nothing is there
	MinTTL time.Duration                                    // shortest usable expiry: tiny for memory, 1s for S3
}

// RunSuite checks the behaviour every blob.Store implementation must share.
// newHarness must return a fresh harness for each call.
func RunSuite(t *testing.T, newHarness func(t *testing.T) Harness) {
	tests := []struct {
		name string
		fn   func(*testing.T, Harness)
	}{
		{"PutThenStat", testPutThenStat},
		{"PutThenGet", testPutThenGet},
		{"StatMissing", testStatMissing},
		{"GetMissing", testGetMissing},
		{"Delete", testDelete},
		{"RejectsWrongSize", testRejectsWrongSize},
		{"RejectsWrongContentType", testRejectsWrongContentType},
		{"RejectsWrongMethod", testRejectsWrongMethod},
		{"RejectsExpired", testRejectsExpired},
		{"ReturnedBytesAreACopy", testReturnedBytesAreACopy},
		{"AwkwardKeys", testAwkwardKeys},
		{"ReadPrefix", testReadPrefix},
		{"DownloadFilename", testDownloadFilename},
		{"Copy", testCopy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, newHarness(t))
		})
	}
}

const (
	testType = "image/png"
	ttl      = time.Minute // long enough that only the expiry test ever sees a URL expire
)

var testData = []byte("not really a png")

// uniqueKey returns a key no other test run uses, so runs against a real,
// shared bucket never collide.
func uniqueKey() string {
	return "test/" + rand.Text()
}

// mustUpload presigns a PUT for exactly data and uploads it, or fails the test.
func mustUpload(t *testing.T, h Harness, key, contentType string, data []byte) {
	t.Helper()
	url, err := h.Store.PresignPut(t.Context(), h.Bucket, key, contentType, int64(len(data)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := h.Put(url, contentType, data); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

// mustDownload presigns a GET and downloads, or fails the test.
func mustDownload(t *testing.T, h Harness, key string) []byte {
	t.Helper()
	url, err := h.Store.PresignGet(t.Context(), h.Bucket, key, ttl, "")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	data, _, err := h.Get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	return data
}

// assertNotStored fails the test if anything is stored at key. Used after
// every rejected upload: returning an error isn't enough, nothing may be saved.
func assertNotStored(t *testing.T, h Harness, key string) {
	t.Helper()
	if info, err := h.Store.Stat(t.Context(), h.Bucket, key); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Stat after rejected upload = %+v, %v; want ErrNotFound", info, err)
	}
}

func testPutThenStat(t *testing.T, h Harness) {
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	info, err := h.Store.Stat(t.Context(), h.Bucket, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	want := blob.Info{Size: int64(len(testData)), ContentType: testType}
	if info != want {
		t.Errorf("Stat = %+v, want %+v", info, want)
	}
}

func testPutThenGet(t *testing.T, h Harness) {
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	if got := mustDownload(t, h, key); !bytes.Equal(got, testData) {
		t.Errorf("downloaded %q, want %q", got, testData)
	}
}

func testStatMissing(t *testing.T, h Harness) {
	_, err := h.Store.Stat(t.Context(), h.Bucket, uniqueKey())
	if !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Stat = %v, want ErrNotFound", err)
	}
}

// Presigning doesn't check existence (S3 signs locally); the download fails.
func testGetMissing(t *testing.T, h Harness) {
	url, err := h.Store.PresignGet(t.Context(), h.Bucket, uniqueKey(), ttl, "")
	if err != nil {
		t.Fatalf("PresignGet for a missing key should still succeed: %v", err)
	}
	data, _, err := h.Get(url)
	if !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("download = %q, %v; want ErrNotFound", data, err)
	}
}

func testDelete(t *testing.T, h Harness) {
	ctx := t.Context()
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	if err := h.Store.Delete(ctx, h.Bucket, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.Store.Stat(ctx, h.Bucket, key); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrNotFound", err)
	}
	// Deleting again, or something that never existed, is not an error.
	if err := h.Store.Delete(ctx, h.Bucket, key); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	if err := h.Store.Delete(ctx, h.Bucket, uniqueKey()); err != nil {
		t.Errorf("Delete of a never-existing key: %v", err)
	}
}

func testRejectsWrongSize(t *testing.T, h Harness) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"one byte short", testData[:len(testData)-1]},
		{"one byte long", append(bytes.Clone(testData), 'x')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := uniqueKey()
			// Signed for len(testData) bytes; tc.data is a different length.
			url, err := h.Store.PresignPut(t.Context(), h.Bucket, key, testType, int64(len(testData)), ttl)
			if err != nil {
				t.Fatalf("PresignPut: %v", err)
			}
			if err := h.Put(url, testType, tc.data); err == nil {
				t.Error("upload with the wrong size succeeded")
			}
			assertNotStored(t, h, key)
		})
	}
}

func testRejectsWrongContentType(t *testing.T, h Harness) {
	key := uniqueKey()
	url, err := h.Store.PresignPut(t.Context(), h.Bucket, key, testType, int64(len(testData)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := h.Put(url, "image/jpeg", testData); err == nil {
		t.Error("upload with the wrong content type succeeded")
	}
	assertNotStored(t, h, key)
}

func testRejectsWrongMethod(t *testing.T, h Harness) {
	ctx := t.Context()
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	putURL, err := h.Store.PresignPut(ctx, h.Bucket, key, testType, int64(len(testData)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if _, _, err := h.Get(putURL); err == nil {
		t.Error("downloading through an upload URL succeeded")
	}

	other := uniqueKey()
	getURL, err := h.Store.PresignGet(ctx, h.Bucket, other, ttl, "")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if err := h.Put(getURL, testType, testData); err == nil {
		t.Error("uploading through a download URL succeeded")
	}
	assertNotStored(t, h, other)
}

func testRejectsExpired(t *testing.T, h Harness) {
	if testing.Short() && h.MinTTL >= time.Second {
		t.Skip("waits for a URL to expire; skipped with -short")
	}
	key := uniqueKey()
	url, err := h.Store.PresignPut(t.Context(), h.Bucket, key, testType, int64(len(testData)), h.MinTTL)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	time.Sleep(h.MinTTL + h.MinTTL/2 + 10*time.Millisecond)
	if err := h.Put(url, testType, testData); err == nil {
		t.Error("upload through an expired URL succeeded")
	}
	assertNotStored(t, h, key)
}

func testReturnedBytesAreACopy(t *testing.T, h Harness) {
	key := uniqueKey()
	data := bytes.Clone(testData)
	mustUpload(t, h, key, testType, data)
	data[0] = 'X' // the caller reusing its buffer must not change what's stored

	first := mustDownload(t, h, key)
	first[0] = 'Y' // nor may changing what a download returned
	if second := mustDownload(t, h, key); !bytes.Equal(second, testData) {
		t.Errorf("stored bytes changed to %q, want %q", second, testData)
	}
}

// Keys with characters that need URL escaping must round-trip unchanged.
func testAwkwardKeys(t *testing.T, h Harness) {
	key := uniqueKey() + "/a b?c#d%e/ñ.png"
	mustUpload(t, h, key, testType, testData)

	if got := mustDownload(t, h, key); !bytes.Equal(got, testData) {
		t.Errorf("downloaded %q, want %q", got, testData)
	}
	if _, err := h.Store.Stat(t.Context(), h.Bucket, key); err != nil {
		t.Errorf("Stat: %v", err)
	}
}

// ReadPrefix returns just the start of a file, for type sniffing.
func testReadPrefix(t *testing.T, h Harness) {
	ctx := t.Context()
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	head, err := h.Store.ReadPrefix(ctx, h.Bucket, key, 4)
	if err != nil || !bytes.Equal(head, testData[:4]) {
		t.Errorf("ReadPrefix(4) = %q, %v; want %q", head, err, testData[:4])
	}
	// Asking for more than the file holds returns the whole file.
	all, err := h.Store.ReadPrefix(ctx, h.Bucket, key, 1<<20)
	if err != nil || !bytes.Equal(all, testData) {
		t.Errorf("ReadPrefix(1MiB) = %q, %v; want the whole file", all, err)
	}
	head[0] = 'X' // the returned bytes are the caller's to change
	if again, _ := h.Store.ReadPrefix(ctx, h.Bucket, key, 4); !bytes.Equal(again, testData[:4]) {
		t.Errorf("stored bytes changed to %q through a returned prefix", again)
	}
	if _, err := h.Store.ReadPrefix(ctx, h.Bucket, uniqueKey(), 4); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("ReadPrefix of a missing key = %v, want ErrNotFound", err)
	}
}

// A signed filename makes the browser save the download under that name.
func testDownloadFilename(t *testing.T, h Harness) {
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)
	for _, name := range []string{"Logo.png", "Ärger & Co (final).png", ""} {
		url, err := h.Store.PresignGet(t.Context(), h.Bucket, key, ttl, name)
		if err != nil {
			t.Fatalf("PresignGet: %v", err)
		}
		data, got, err := h.Get(url)
		if err != nil || got != name || !bytes.Equal(data, testData) {
			t.Errorf("download with filename %q = %q, %q, %v", name, data, got, err)
		}
	}
}

// Copy makes a second, independent object with the same bytes and type.
func testCopy(t *testing.T, h Harness) {
	ctx := t.Context()
	src, dst := uniqueKey(), uniqueKey()+"/a b+c/ñ.png" // awkward: S3's CopySource must be escaped
	mustUpload(t, h, src, testType, testData)

	if err := h.Store.Copy(ctx, h.Bucket, src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := mustDownload(t, h, dst); !bytes.Equal(got, testData) {
		t.Errorf("copy holds %q, want %q", got, testData)
	}
	if info, err := h.Store.Stat(ctx, h.Bucket, dst); err != nil || info.Size != int64(len(testData)) || info.ContentType != testType {
		t.Errorf("Stat(copy) = %+v, %v; want size %d, type %s", info, err, len(testData), testType)
	}

	// Independent: replacing, then deleting, the source leaves the copy alone.
	mustUpload(t, h, src, testType, []byte("replaced"))
	if err := h.Store.Delete(ctx, h.Bucket, src); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := mustDownload(t, h, dst); !bytes.Equal(got, testData) {
		t.Errorf("after changing the source the copy holds %q, want %q", got, testData)
	}

	// Copying onto an existing object overwrites it, so retries are harmless.
	other, newer := uniqueKey(), []byte("a newer file")
	mustUpload(t, h, other, "image/webp", newer)
	if err := h.Store.Copy(ctx, h.Bucket, other, dst); err != nil {
		t.Fatalf("Copy onto an existing key: %v", err)
	}
	if got := mustDownload(t, h, dst); !bytes.Equal(got, newer) {
		t.Errorf("overwritten copy holds %q, want %q", got, newer)
	}
	if info, err := h.Store.Stat(ctx, h.Bucket, dst); err != nil || info.ContentType != "image/webp" {
		t.Errorf("Stat(overwritten copy) = %+v, %v; want type image/webp", info, err)
	}

	missing := uniqueKey()
	if err := h.Store.Copy(ctx, h.Bucket, uniqueKey(), missing); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Copy of a missing source = %v, want ErrNotFound", err)
	}
	assertNotStored(t, h, missing)
	if err := h.Store.Copy(ctx, h.Bucket, other, other); err == nil {
		t.Error("Copy onto itself: want an error")
	}
}
