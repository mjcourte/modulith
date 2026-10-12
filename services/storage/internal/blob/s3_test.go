package blob_test

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"testing"
	"time"

	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/blob/blobtest"
)

// TestS3 runs the shared suite against a real S3-compatible server, e.g. the
// RustFS in deploy/monolith/compose.yaml:
//
//	GYOZA_TEST_S3_ENDPOINT=http://localhost:9000
//
// Credentials default to the compose file's; override with
// GYOZA_TEST_S3_ACCESS_KEY / GYOZA_TEST_S3_SECRET_KEY.
func TestS3(t *testing.T) {
	endpoint := os.Getenv("GYOZA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set GYOZA_TEST_S3_ENDPOINT to run against S3")
	}
	store, err := blob.NewS3(blob.S3Config{
		Endpoint:  endpoint,
		AccessKey: envOr("GYOZA_TEST_S3_ACCESS_KEY", "gyoza"),
		SecretKey: envOr("GYOZA_TEST_S3_SECRET_KEY", "gyoza-dev-secret"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	const bucket = "gyoza-test"
	if err := store.EnsureBucket(t.Context(), bucket); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	blobtest.RunSuite(t, func(*testing.T) blobtest.Harness {
		return blobtest.Harness{Store: store, Bucket: bucket, Put: httpPut, Get: httpGet, MinTTL: time.Second}
	})
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// httpPut uploads as a browser would: a plain PUT to the presigned URL.
func httpPut(url, contentType string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT: %s: %s", resp.Status, body)
	}
	return nil
}

// httpGet downloads as a browser would, returning the bytes and the name the
// browser would save them under (from Content-Disposition).
func httpGet(url string) ([]byte, string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", blob.ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("GET: %s: %s", resp.Status, body)
	}
	filename := ""
	if disp := resp.Header.Get("Content-Disposition"); disp != "" {
		// ParseMediaType decodes filename*=utf-8''... into params["filename"].
		_, params, err := mime.ParseMediaType(disp)
		if err != nil {
			return nil, "", fmt.Errorf("parse Content-Disposition %q: %w", disp, err)
		}
		filename = params["filename"]
	}
	return body, filename, nil
}
