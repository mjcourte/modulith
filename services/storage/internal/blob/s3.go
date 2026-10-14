package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
)

// S3Config says how to reach an S3-compatible service.
type S3Config struct {
	// Endpoint is the service URL, e.g. "http://localhost:9000" for the
	// local RustFS; empty for AWS itself.
	Endpoint string
	// Region defaults to "us-east-1". S3-compatible servers usually accept any.
	Region string
	// AccessKey and SecretKey are static credentials. (Production on AWS
	// will want the default credential chain, e.g. an ECS task role: step 10.)
	AccessKey string
	SecretKey string
	// PathStyle puts the bucket in the path (http://host/bucket/key) rather
	// than the host name; needed by most S3-compatible servers.
	PathStyle bool
}

// S3 is a Store backed by Amazon S3 or an S3-compatible server.
type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
}

// Compile-time check that *S3 satisfies Store.
var _ Store = (*S3)(nil)

// NewS3 returns an S3 store. It doesn't contact the service.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("blob: S3 needs an access key and secret key")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	opts := s3.Options{
		Region:       cfg.Region,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		UsePathStyle: cfg.PathStyle,
		// Since 2025 the SDK adds checksum headers to uploads by default. In a
		// presigned URL that would oblige the browser to send a checksum it
		// can't know, so only add them where S3 requires it.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	// One span per S3 call (a no-op until telemetry is set up). Presigned
	// URLs aren't traced: the browser uses them, not this client.
	otelaws.AppendMiddlewares(&opts.APIOptions)
	client := s3.New(opts)
	return &S3{client: client, presign: s3.NewPresignClient(client)}, nil
}

func (s *S3) PresignPut(ctx context.Context, bucket, key, contentType string, size int64, ttl time.Duration) (string, error) {
	// Content-Type and Content-Length are signed into the URL, so S3 refuses
	// any other type or size.
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("blob: presign put: %w", err)
	}
	return req.URL, nil
}

func (s *S3) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration, filename string) (string, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if filename != "" {
		// S3 serves this as the response's Content-Disposition. FormatMediaType
		// encodes non-ASCII names the RFC 2231/5987 way (filename*=utf-8''...),
		// which browsers understand.
		in.ResponseContentDisposition = aws.String(mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	}
	req, err := s.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("blob: presign get: %w", err)
	}
	return req.URL, nil
}

func (s *S3) Stat(ctx context.Context, bucket, key string) (Info, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if isStatus(err, http.StatusNotFound) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, fmt.Errorf("blob: stat: %w", err)
	}
	return Info{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}, nil
}

func (s *S3) ReadPrefix(ctx context.Context, bucket, key string, n int) ([]byte, error) {
	if n < 1 {
		return nil, fmt.Errorf("blob: read prefix: n must be positive, got %d", n)
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", n-1)), // only the first n bytes leave S3
	})
	switch {
	case isStatus(err, http.StatusNotFound):
		return nil, ErrNotFound
	case isStatus(err, http.StatusRequestedRangeNotSatisfiable):
		return []byte{}, nil // the object exists but is empty: there's no byte 0
	case err != nil:
		return nil, fmt.Errorf("blob: read prefix: %w", err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, int64(n)))
	if err != nil {
		return nil, fmt.Errorf("blob: read prefix: %w", err)
	}
	return data, nil
}

func (s *S3) Delete(ctx context.Context, bucket, key string) error {
	// S3 answers deleting a missing key with success, matching Store's contract.
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("blob: delete: %w", err)
	}
	return nil
}

func (s *S3) Copy(ctx context.Context, bucket, srcKey, dstKey string) error {
	if srcKey == dstKey {
		return errCopyOntoItself // S3 refuses it too, with a 400 that's awkward to match
	}
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(copySource(bucket, srcKey)),
		// MetadataDirective unset means COPY: the content type comes along.
	})
	if isStatus(err, http.StatusNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("blob: copy: %w", err)
	}
	return nil
}

// copySource is CopyObject's "bucket/key". Unlike Key, which the SDK encodes,
// it's sent as given, so each segment is escaped here; the slashes stay.
func copySource(bucket, key string) string {
	parts := strings.Split(bucket+"/"+key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// EnsureBucket creates bucket if it doesn't exist yet: for local development
// and tests. Production buckets are made by infrastructure code, not the app.
func (s *S3) EnsureBucket(ctx context.Context, bucket string) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if err == nil || errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return fmt.Errorf("blob: create bucket %s: %w", bucket, err)
}

// isStatus reports whether err is an S3 response with the given HTTP status.
// Checking the status, not error type names, works the same across S3 and
// S3-compatible servers (HEAD responses carry no error body to name a type).
func isStatus(err error, status int) bool {
	var resp *smithyhttp.ResponseError
	return errors.As(err, &resp) && resp.HTTPStatusCode() == status
}
