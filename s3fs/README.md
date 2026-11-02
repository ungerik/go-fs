# S3 File System

Objects in Amazon S3 and S3-compatible stores as
[go-fs](https://github.com/ungerik/go-fs) files: `s3://bucket/report.pdf` is
read, written, listed, copied and moved like a local path.
[S3-compatible services](#s3-compatible-services) has the client settings for
Cloudflare R2, Backblaze B2, Wasabi, DigitalOcean Spaces, Google Cloud Storage
and Hetzner Object Storage, and s3fs copies large objects also where the
service refuses to copy them on the server.

## Installation

Get the backend together with the go-fs core module, at the same version:

```bash
go get github.com/ungerik/go-fs@v1.0.0-rc.5 github.com/ungerik/go-fs/s3fs@v1.0.0-rc.5
```

## Usage

```go
import (
    "context"

    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/s3fs"
)

func main() {
    ctx := context.Background()

    // Create an S3 client using the default AWS credential chain
    cfg, err := config.LoadDefaultConfig(ctx)
    if err != nil {
        panic(err)
    }
    client := s3.NewFromConfig(cfg)

    // Create the file system for a bucket and register it,
    // Files resolve to their file system with the registry
    bucket, err := s3fs.New(client, "my-bucket", nil)
    if err != nil {
        panic(err)
    }
    err = fs.Register(bucket)
    if err != nil {
        panic(err)
    }
    defer bucket.Close() // Also unregisters it

    // Use it like any other go-fs file system
    file := fs.File("s3://my-bucket/path/to/file.txt")

    err = file.WriteAllString(ctx, "Hello, S3!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1)
}
```

- `s3fs.New(client, bucket, opts)` returns the file system without
  registering it and sends no request.
- `s3fs.NewAndRegister(client, bucket, opts)` creates and registers it in
  one step and returns only an error. It wraps `fs.ErrAlreadyRegistered` if
  another file system holds the prefix. To close and unregister it later,
  get it with `fs.GetFileSystemByPrefixOrNil("s3://my-bucket")`, or use
  `New` and `fs.Register` to keep the instance.
- `s3fs.NewLoadDefaultConfig(ctx, bucket, opts)` loads the default AWS
  configuration and creates the client in one step, like `New` it doesn't
  register the file system.

File URIs have the form `s3://bucket-name/key`, the same format the AWS CLI
and SDKs use. `Name()` is `"S3"`, `ID()` the bucket name.

### Options

`nil` or zero fields use the defaults:

```go
bucket, err := s3fs.New(client, "my-bucket", &s3fs.Options{
    ReadOnly:                   true,      // writes return fs.ErrReadOnlyFileSystem
    MultipartUploadThreshold:   16 << 20,  // multipart uploads from 16 MiB on
    MultipartDownloadThreshold: 16 << 20,  // ReadAll in ranges from 16 MiB on
    PartSize:                   8 << 20,   // part and range size (S3: at least 5 MiB)
    Concurrency:                5,         // parts in flight per transfer
    Prefix:                     "s3://minio.example.com/my-bucket",
})
```

`Prefix` replaces the default `"s3://" + bucket`, for buckets with the same
name at different endpoints (an AWS bucket and a MinIO bucket called
`backup`). `ID()` is the prefix then.

### Credentials

The AWS SDK for Go v2 resolves credentials from environment variables
(`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`), the
shared credentials file, IAM roles, or static credentials:

```go
client := s3.New(s3.Options{
    Region: "us-east-1",
    Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
})
```

See the [AWS SDK for Go v2 configuration guide](https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk/).

### S3-compatible services

Point the client at the service endpoint. Self-hosted services like MinIO
or Ceph RGW usually need `UsePathStyle: true`. Services that don't verify the
checksums the SDK sends by default, or reject them, need the checksum
options set to `WhenRequired`. s3fs applies the options of the client to
its multipart uploads and ranged downloads too. A client of `s3.New`
without these options sends no checksums; `config.LoadDefaultConfig` turns
them on, set `config.WithRequestChecksumCalculation` and
`config.WithResponseChecksumValidation` there instead.

```go
client := s3.New(s3.Options{
    Region:       "auto",
    Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
    BaseEndpoint: new("https://ACCOUNT_ID.r2.cloudflarestorage.com"),
    // Only for services without support for the default checksums
    RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
    ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
})
```

| Service | `BaseEndpoint` | `Region` | `UsePathStyle` | Checksums |
|---|---|---|---|---|
| Amazon S3 | not set | bucket region | `false` | SDK default |
| Cloudflare R2 | `https://ACCOUNT_ID.r2.cloudflarestorage.com` | `auto` | either | `WhenRequired` |
| Backblaze B2 | `https://s3.REGION.backblazeb2.com` | the region of the endpoint, like `us-west-004` | `true` for bucket names with dots | `WhenRequired`, required |
| Wasabi | `https://s3.REGION.wasabisys.com`, the region of the bucket | the region of the bucket | `true` | `WhenRequired` |
| DigitalOcean Spaces | `https://REGION.digitaloceanspaces.com` | `us-east-1` | `true` for bucket names with dots | `WhenRequired` |
| Google Cloud Storage | `https://storage.googleapis.com` | `auto` | `true` | `WhenRequired`, required, plus the signing fix below |
| Hetzner Object Storage | `https://LOCATION.your-objectstorage.com`, like `fsn1` | the location, like `fsn1` | `false` | `WhenRequired` |

What else to know about the services:

- **Cloudflare R2** allows one write per second to the same key and answers
  more with `429 TooManyRequests`, which the SDK doesn't retry by default:
  set `Retryer: retry.AddWithErrorCodes(retry.NewStandard(), "TooManyRequests")`
  (`github.com/aws/aws-sdk-go-v2/aws/retry`). R2 matches keys by their Unicode
  normalization, so `"café"` written with a combining accent and without one
  is the same object; `Move` of a file between the two spellings is refused,
  because removing the source would remove the moved file.
- **Backblaze B2** keeps every version of an object: overwrites, `Touch` and
  deletes leave old versions that are billed until a lifecycle rule removes them.
- **Wasabi** bills objects for 90 days (30 days with reserved capacity),
  also when they are deleted or overwritten before: `Touch`, `Move` and
  overwrites of younger objects cost the remaining days. Keys with
  characters outside the Basic Multilingual Plane, like emoji, are rejected.
- **DigitalOcean Spaces** Cold Storage buckets allow 25 listings per second,
  and every write lists the key once to check that it is no directory.
- **Google Cloud Storage** changes the `Accept-Encoding` header after the SDK
  signed it, so every request fails with `403 SignatureDoesNotMatch` unless
  the header is left out of the signature. Add this middleware to the
  `APIOptions` of the client:

  ```go
  type acceptEncodingKey struct{}

  // excludeAcceptEncodingFromSignature removes Accept-Encoding before
  // the request is signed and restores it after.
  func excludeAcceptEncodingFromSignature(stack *middleware.Stack) error {
      err := stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("RemoveAcceptEncoding",
          func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
              if req, ok := in.Request.(*smithyhttp.Request); ok {
                  ctx = middleware.WithStackValue(ctx, acceptEncodingKey{}, req.Header.Get("Accept-Encoding"))
                  req.Header.Del("Accept-Encoding")
              }
              return next.HandleFinalize(ctx, in)
          }), "Signing", middleware.Before)
      if err != nil {
          return err
      }
      return stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("RestoreAcceptEncoding",
          func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
              if req, ok := in.Request.(*smithyhttp.Request); ok {
                  if value, _ := middleware.GetStackValue(ctx, acceptEncodingKey{}).(string); value != "" {
                      req.Header.Set("Accept-Encoding", value)
                  }
              }
              return next.HandleFinalize(ctx, in)
          }), "Signing", middleware.After)
  }
  ```

  `middleware` is `github.com/aws/smithy-go/middleware`, `smithyhttp` is
  `github.com/aws/smithy-go/transport/http`. Use HMAC keys as credentials.
  Google Cloud Storage has no `UploadPartCopy`; s3fs recognizes its endpoints
  and copies objects of every size with `CopyObject`. It has no
  `DeleteObjects` either, `RemoveAll` deletes the objects one by one there.
  Incomplete multipart uploads never expire there, a lifecycle rule should
  delete them.

## How S3 concepts map to the file system

- **Paths and keys.** File system paths are rooted (`/docs/readme.txt`),
  object keys are not (`docs/readme.txt`). Keys that no path maps to,
  because they start with a slash or contain an empty element
  (`/x.txt`, `docs//x.txt`), are skipped by the listings, see
  [Migrating from v0](#migrating-from-v0). The file system implements
  `fs.ValidateNameFileSystem`, so a copy or move into it fails before it
  writes anything at a name that is not valid UTF-8, contains a backslash,
  which S3-compatible servers reject, or a control character, which the XML
  of a listing can't hold, or is longer than the 1024 bytes of a key. The
  server checks the 1024 bytes of a whole key only when it is written.
- **Directories.** S3 has no directories. A prefix with at least one
  object below it is an implicit directory. `MakeDir` creates a zero-byte
  marker object with a trailing slash (`docs/`) so that empty directories
  exist too; `Stat`, `ListDir` and `Remove` treat both the same way.
  `MakeDir` on an existing path wraps `os.ErrExist`, `ListDir` of a missing
  directory wraps `os.ErrNotExist`, `Remove` of a directory with content
  returns `fs.ErrDirectoryNotEmpty`, `RemoveAll` deletes everything below a
  prefix with batched `DeleteObjects` requests. `Remove` and `RemoveAll`
  refuse the root directory with an error wrapping `os.ErrInvalid`;
  `RemoveAll` of it would delete every object of the bucket. Writing or copying onto a
  directory returns `fs.ErrIsDirectory` instead of creating an object that
  would turn the directory into a file; that check costs one
  `ListObjectsV2` request per write.
- **Transfers.** The transfers use the AWS
  [transfer manager](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager).
  `ReadAll` downloads objects from `MultipartDownloadThreshold` on with
  concurrent range requests (all with the ETag of the object, so they read
  one version), `WriteAll` uploads from `MultipartUploadThreshold` on in
  parts. `OpenReader` streams the object body. `OpenWriter` streams the
  written data into an upload: up to `MultipartUploadThreshold` it is
  buffered and uploaded on `Close`, beyond that it is uploaded in parts while
  it is written, so memory stays bounded. The object only appears or changes
  when `Close` completes the upload. The writers implement
  `fs.WriteAborter`: `Abort` cancels the upload and aborts a multipart
  upload, nothing is committed. `OpenReadWriter` buffers the object in
  memory and uploads it on `Close`.
- **Copy and touch.** `CopyFile` is a server-side `CopyObject`, objects
  above 5 GiB are copied in parts with `UploadPartCopy`. The copy keeps the
  storage class and the server-side encryption of the source. A service that
  refuses the copy with `501 Not Implemented` or `413`, like Cloudflare R2 and
  Backblaze B2 the conditions of `UploadPartCopy`, gets the object downloaded
  and uploaded, with the metadata of the source but without its tags.
  A download that differs from the object in `Content-Length` or
  `Content-Encoding`, like the decompressed data that Google Cloud Storage
  sends of an object with `Content-Encoding: gzip`, is not uploaded and
  `CopyFile` returns an `fs.ErrUnsupported` error.
  `Touch` of an existing object copies it onto itself, because S3 can't
  update `LastModified` in place. It keeps the `Content-Type` and the other content
  headers, the user metadata, the storage class and the encryption; it
  doesn't keep ACLs, and for objects above 5 GiB not the tags. Services that
  refuse such a copy make it return an `fs.ErrUnsupported` error.
- **Permissions and links.** S3 access is governed by IAM policies, the
  reported permissions are the constants `s3fs.DefaultPermissions` /
  `s3fs.DefaultDirPermissions`. There are no symbolic links and no append;
  `Append` is emulated by rewriting the object. `Move` and `Rename` copy and
  delete.
- **Errors.** Missing objects wrap `os.ErrNotExist`; after `Close` every
  method returns `fs.ErrFileSystemClosed`, also the `Close` of a writer that
  was opened before.

## Migrating from v0

go-fs v0 used the rooted path as object key, so a file `/docs/x.txt`
became the key `/docs/x.txt` with a leading slash. v1 uses `docs/x.txt` like
the AWS CLI and every other S3 tool, and doesn't see the old keys: their
paths would be cleaned to the new keys.

Copy the old objects to the new keys once, for example with the AWS SDK.
`CopyObject` copies objects up to 5 GiB; copy larger ones with a multipart
copy or a tool like [rclone](https://rclone.org).

```go
func migrateLeadingSlashKeys(ctx context.Context, client *s3.Client, bucket string) error {
    paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
        Bucket: new(bucket),
        Prefix: new("/"),
    })
    for paginator.HasMorePages() {
        page, err := paginator.NextPage(ctx)
        if err != nil {
            return err
        }
        for _, obj := range page.Contents {
            oldKey := aws.ToString(obj.Key)
            newKey := strings.TrimLeft(oldKey, "/")
            // The copy source is URL-encoded, S3 decodes a "+" as space
            source := bucket + "/" + strings.ReplaceAll(url.PathEscape(oldKey), "+", "%2B")
            _, err = client.CopyObject(ctx, &s3.CopyObjectInput{
                Bucket:     new(bucket),
                Key:        new(newKey),
                CopySource: new(source),
            })
            if err != nil {
                return fmt.Errorf("copying %q: %w", oldKey, err)
            }
        }
    }
    return nil
}
```

Delete the old keys (the same listing with `DeleteObjects`) after checking
the copies. Objects that v0 and other tools wrote with the same name end up
as one object, the copy replaces the existing one.

## Testing

The tests run the go-fs conformance suite against an
[S3Mock](https://github.com/adobe/S3Mock) container started with Docker
(`adobe/s3mock:5.2.3` on a free local port), plus tests for multipart
uploads, downloads and copies, streaming writers and the regressions of the
v1 review. MinIO is no longer used because its Docker images were removed.
S3Mock accepts any credentials, so authentication is not tested. S3Mock
differs from AWS in two ways the tests work around: it refuses a copy of an
object onto itself without user metadata (AWS accepts it with the `REPLACE`
directive that `Touch` uses), and `AbortMultipartUpload` deletes an existing
object with the key of the upload (AWS keeps it).

Docker is required for most tests. They are skipped when Docker is
unavailable, meaning the `docker` command is not installed or its daemon
doesn't answer `docker info`. When Docker is available but the container
can't be started, the test run fails instead of skipping, except on Windows,
where Docker usually can't run the Linux image and the tests are skipped.
The offline tests (constructors, escaping, writers, and the provider quirks
against a fake store in memory: checksums, abort dates, refused copies,
broken listing pages, delete batches) always run.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
