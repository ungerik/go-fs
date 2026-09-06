# S3 File System

A [go-fs](https://github.com/ungerik/go-fs) file system for Amazon S3 and
S3-compatible object stores (MinIO, DigitalOcean Spaces, Wasabi, Backblaze
B2, ...).

## Installation

```bash
go get github.com/ungerik/go-fs/s3fs
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

    // Create and register the file system for a bucket
    bucket := s3fs.NewAndRegister(client, "my-bucket", false)
    defer bucket.Close()

    // Use it like any other go-fs file system
    file := fs.File("s3://my-bucket/path/to/file.txt")

    err = file.WriteAllString(ctx, "Hello, S3!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1)
}
```

`s3fs.NewLoadDefaultConfig(ctx, "my-bucket", false)` loads the default
configuration and creates the client in one step. The third argument makes
the file system read-only.

File URIs have the form `s3://bucket-name/key`, the same format the AWS CLI
and SDKs use. `bucket.ID()` is the bucket name.

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

Point the client at the service endpoint. MinIO needs path-style URLs:

```go
client := s3.New(s3.Options{
    Region:       "us-east-1",
    Credentials:  credentials.NewStaticCredentialsProvider("minioadmin", "minioadmin", ""),
    BaseEndpoint: aws.String("http://localhost:9000"),
    UsePathStyle: true,
})
```

## How S3 concepts map to the file system

- **Paths and keys.** File system paths are rooted (`/docs/readme.txt`),
  object keys are not (`docs/readme.txt`).
- **Directories.** S3 has no directories. A prefix with at least one
  object below it is an implicit directory. `MakeDir` creates a zero-byte
  marker object with a trailing slash (`docs/`) so that empty directories
  exist too; `Stat`, `ListDir` and `Remove` treat both the same way.
  `MakeDir` on an existing path wraps `os.ErrExist`, `Remove` of a
  directory with content fails, `RemoveAll` deletes everything below a
  prefix with batched `DeleteObjects` requests.
- **Transfers.** `ReadAll` and `WriteAll` use concurrent multipart
  transfers from `s3fs.MultipartDownloadThreshold` (10 MB) and
  `s3fs.MultipartUploadThreshold` (5 MB) on; both are variables.
  `OpenReader` streams the object body, `OpenWriter` and `OpenReadWriter`
  buffer in memory and upload on `Close`.
- **Copy and touch.** `CopyFile` is a server-side `CopyObject`. `Touch` of
  an existing object copies it onto itself, because S3 can't update
  `LastModified` in place.
- **Permissions and links.** S3 access is governed by IAM policies, the
  reported permissions are `s3fs.DefaultPermissions` /
  `s3fs.DefaultDirPermissions`. There are no symbolic links and no append;
  `Append` is emulated by rewriting the object.
- **Errors.** Missing objects wrap `os.ErrNotExist`; after `Close` every
  method returns `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against a MinIO container
started with Docker (`minio/minio:latest` on ports 9000 and 9001) and a
multipart transfer test with an 11 MB object. Docker is required; if it is
not installed the tests are skipped, if it is installed but the container
can't be started the tests fail.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
