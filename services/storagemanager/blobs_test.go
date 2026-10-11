package storagemanager_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"gyoza/services/storagemanager"
)

// blobsHarness is one Blobs implementation plus a way to act as the browser
// against its presigned URLs.
type blobsHarness struct {
	blobs  storagemanager.Blobs
	bucket string                                           // must already exist (matters for S3)
	put    func(url, contentType string, data []byte) error // browser upload
	get    func(url string) ([]byte, error)                 // browser download; ErrBlobNotFound when nothing is there
	minTTL time.Duration                                    // shortest usable expiry: tiny for memory, 1s for S3
}

// runBlobsTests checks the behaviour every Blobs implementation must share.
// newHarness must return a fresh harness for each call.
func runBlobsTests(t *testing.T, newHarness func(t *testing.T) blobsHarness) {
	tests := []struct {
		name string
		fn   func(*testing.T, blobsHarness)
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
func mustUpload(t *testing.T, h blobsHarness, key, contentType string, data []byte) {
	t.Helper()
	url, err := h.blobs.PresignPut(t.Context(), h.bucket, key, contentType, int64(len(data)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := h.put(url, contentType, data); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

// mustDownload presigns a GET and downloads, or fails the test.
func mustDownload(t *testing.T, h blobsHarness, key string) []byte {
	t.Helper()
	url, err := h.blobs.PresignGet(t.Context(), h.bucket, key, ttl)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	data, err := h.get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	return data
}

// assertNotStored fails the test if anything is stored at key. Used after
// every rejected upload: returning an error isn't enough, nothing may be saved.
func assertNotStored(t *testing.T, h blobsHarness, key string) {
	t.Helper()
	if info, err := h.blobs.Stat(t.Context(), h.bucket, key); !errors.Is(err, storagemanager.ErrBlobNotFound) {
		t.Errorf("Stat after rejected upload = %+v, %v; want ErrBlobNotFound", info, err)
	}
}

func testPutThenStat(t *testing.T, h blobsHarness) {
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	info, err := h.blobs.Stat(t.Context(), h.bucket, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	want := storagemanager.BlobInfo{Size: int64(len(testData)), ContentType: testType}
	if info != want {
		t.Errorf("Stat = %+v, want %+v", info, want)
	}
}

func testPutThenGet(t *testing.T, h blobsHarness) {
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	if got := mustDownload(t, h, key); !bytes.Equal(got, testData) {
		t.Errorf("downloaded %q, want %q", got, testData)
	}
}

func testStatMissing(t *testing.T, h blobsHarness) {
	_, err := h.blobs.Stat(t.Context(), h.bucket, uniqueKey())
	if !errors.Is(err, storagemanager.ErrBlobNotFound) {
		t.Errorf("Stat = %v, want ErrBlobNotFound", err)
	}
}

// Presigning doesn't check existence (S3 signs locally); the download fails.
func testGetMissing(t *testing.T, h blobsHarness) {
	url, err := h.blobs.PresignGet(t.Context(), h.bucket, uniqueKey(), ttl)
	if err != nil {
		t.Fatalf("PresignGet for a missing key should still succeed: %v", err)
	}
	data, err := h.get(url)
	if !errors.Is(err, storagemanager.ErrBlobNotFound) {
		t.Errorf("download = %q, %v; want ErrBlobNotFound", data, err)
	}
}

func testDelete(t *testing.T, h blobsHarness) {
	ctx := t.Context()
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	if err := h.blobs.Delete(ctx, h.bucket, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.blobs.Stat(ctx, h.bucket, key); !errors.Is(err, storagemanager.ErrBlobNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrBlobNotFound", err)
	}
	// Deleting again, or something that never existed, is not an error.
	if err := h.blobs.Delete(ctx, h.bucket, key); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	if err := h.blobs.Delete(ctx, h.bucket, uniqueKey()); err != nil {
		t.Errorf("Delete of a never-existing key: %v", err)
	}
}

func testRejectsWrongSize(t *testing.T, h blobsHarness) {
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
			url, err := h.blobs.PresignPut(t.Context(), h.bucket, key, testType, int64(len(testData)), ttl)
			if err != nil {
				t.Fatalf("PresignPut: %v", err)
			}
			if err := h.put(url, testType, tc.data); err == nil {
				t.Error("upload with the wrong size succeeded")
			}
			assertNotStored(t, h, key)
		})
	}
}

func testRejectsWrongContentType(t *testing.T, h blobsHarness) {
	key := uniqueKey()
	url, err := h.blobs.PresignPut(t.Context(), h.bucket, key, testType, int64(len(testData)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if err := h.put(url, "image/jpeg", testData); err == nil {
		t.Error("upload with the wrong content type succeeded")
	}
	assertNotStored(t, h, key)
}

func testRejectsWrongMethod(t *testing.T, h blobsHarness) {
	ctx := t.Context()
	key := uniqueKey()
	mustUpload(t, h, key, testType, testData)

	putURL, err := h.blobs.PresignPut(ctx, h.bucket, key, testType, int64(len(testData)), ttl)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if _, err := h.get(putURL); err == nil {
		t.Error("downloading through an upload URL succeeded")
	}

	other := uniqueKey()
	getURL, err := h.blobs.PresignGet(ctx, h.bucket, other, ttl)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if err := h.put(getURL, testType, testData); err == nil {
		t.Error("uploading through a download URL succeeded")
	}
	assertNotStored(t, h, other)
}

func testRejectsExpired(t *testing.T, h blobsHarness) {
	if testing.Short() && h.minTTL >= time.Second {
		t.Skip("waits for a URL to expire; skipped with -short")
	}
	key := uniqueKey()
	url, err := h.blobs.PresignPut(t.Context(), h.bucket, key, testType, int64(len(testData)), h.minTTL)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	time.Sleep(h.minTTL + h.minTTL/2 + 10*time.Millisecond)
	if err := h.put(url, testType, testData); err == nil {
		t.Error("upload through an expired URL succeeded")
	}
	assertNotStored(t, h, key)
}

func testReturnedBytesAreACopy(t *testing.T, h blobsHarness) {
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
func testAwkwardKeys(t *testing.T, h blobsHarness) {
	key := uniqueKey() + "/a b?c#d%e/ñ.png"
	mustUpload(t, h, key, testType, testData)

	if got := mustDownload(t, h, key); !bytes.Equal(got, testData) {
		t.Errorf("downloaded %q, want %q", got, testData)
	}
	if _, err := h.blobs.Stat(t.Context(), h.bucket, key); err != nil {
		t.Errorf("Stat: %v", err)
	}
}
