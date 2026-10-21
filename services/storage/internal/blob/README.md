# storage/internal/blob

File storage addressed by bucket and key: S3 in production, an in-memory fake in tests and local runs. Internal to storage, the only service that talks to S3: other services go through `StorageService`, and Go refuses imports of this package from outside `services/storage`.

- `blob.Store`: presign PUT (exact size and content type signed in), presign GET (optional download filename), `Stat`, `ReadPrefix` (first n bytes, for type sniffing), `Delete`
- mechanism only: callers choose buckets, keys and URL lifetimes (storage's key layout lives in its README)
- `blob.Memory`: the fake. Its URLs use a `memory://` scheme; `PutSigned`/`GetSigned` play the browser and enforce what each URL was signed for, as S3 would
- `blobtest.RunSuite`: the shared suite every implementation must pass. A separate package (like `net/http/httptest`) so `testing` is never compiled into production binaries
- `blob.S3` (`s3.go`): Amazon S3 or any S3-compatible server, via aws-sdk-go-v2. `NewS3(S3Config{Endpoint, Region, AccessKey, SecretKey, PathStyle})`; `EnsureBucket` creates a bucket for local runs and tests (production buckets come from infrastructure code)

## S3 notes

- presigned PUTs sign `Content-Type` and `Content-Length`, so S3 itself refuses another type or size. The client is built with `RequestChecksumCalculationWhenRequired`; otherwise the SDK would sign checksum headers a browser can't send
- presigned GETs carry the download name as a signed `response-content-disposition`; `mime.FormatMediaType` encodes non-ASCII names as RFC 5987 `filename*=utf-8''...`
- `ReadPrefix` is a Range GET (`bytes=0-(n-1)`), capped again with `io.LimitReader` in case a server ignores the range. An empty object answers 416, which means "no bytes", not an error
- "not found" is decided by HTTP status (404), not error type names: HEAD responses have no body, and S3-compatible servers name errors differently
- `Delete` of a missing key succeeds, as on S3
- static credentials only for now; on AWS (ECS task roles) the default credential chain comes with the binaries (storage, phase 1 step 10)

## Testing against S3

`s3_test.go` runs `blobtest.RunSuite` against a real server, playing the browser with plain `net/http` PUT/GET. It's skipped unless `MODULITH_TEST_S3_ENDPOINT` is set; credentials default to the compose file's (override with `MODULITH_TEST_S3_ACCESS_KEY` / `MODULITH_TEST_S3_SECRET_KEY`), bucket `modulith-test`.

```
docker compose -f deploy/monolith/compose.yaml up -d --wait
MODULITH_TEST_S3_ENDPOINT=http://localhost:9000 go test ./services/storage/internal/blob -run TestS3 -v
```

Local S3 is RustFS 1.0.0 (Apache-2.0; MinIO no longer publishes community images). It enforces signed size, content type, expiry and method like S3: all 15 suite cases pass. Mutation check: 7/9 planted bugs caught. The two survivors each removed one of the two prefix limits (Range header, `LimitReader`), which back each other up, so no test can tell them apart.

Performance notes: [OPTIMIZATIONS.md](OPTIMIZATIONS.md)
