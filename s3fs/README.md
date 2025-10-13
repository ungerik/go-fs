# S3 File System

A [go-fs](https://github.com/ungerik/go-fs) implementation for Amazon S3 and S3-compatible object storage services.

## Features

- ✅ Full S3 file system interface implementation
- ✅ Automatic multipart upload/download for large files
- ✅ Support for AWS S3 and S3-compatible services (MinIO, DigitalOcean Spaces, etc.)
- ✅ Streaming reads and writes
- ✅ Directory operations (list, recursive list, pattern matching)
- ✅ Metadata operations (stat, touch, copy)
- ✅ Read-only mode support

## Installation

```bash
go get github.com/ungerik/go-fs/s3fs
```

## Usage

### Basic Usage

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

    // Create S3 client using default AWS credentials
    cfg, err := config.LoadDefaultConfig(ctx)
    if err != nil {
        panic(err)
    }
    client := s3.NewFromConfig(cfg)

    // Create and register S3 filesystem
    s3FileSystem := s3fs.NewAndRegister(client, "my-bucket", false)
    defer s3FileSystem.Close()

    // Use like any other go-fs filesystem
    file := fs.File("s3://my-bucket/path/to/file.txt")

    // Write data
    err = file.WriteAllString(ctx, "Hello, S3!", nil)

    // Read data
    content, err := file.ReadAllString(ctx)

    // List directory
    files, err := file.Dir().ListDirMax(ctx, -1, nil)
}
```

### Using Convenience Constructor

```go
// Load default AWS config and create filesystem in one step
s3fs, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", false)
if err != nil {
    panic(err)
}
defer s3fs.Close()
```

### Working with Large Files

The package automatically uses multipart upload/download for better performance:

- Files ≥ 5 MB use multipart upload (configurable via `s3fs.MultipartUploadThreshold`)
- Files ≥ 10 MB use multipart download (configurable via `s3fs.MultipartDownloadThreshold`)

```go
// Large file operations automatically use multipart transfers
largeFile := fs.File("s3://my-bucket/large-file.zip")

// Multipart upload happens automatically
err := largeFile.WriteAll(ctx, largeData, nil)

// Multipart download happens automatically
data, err := largeFile.ReadAll(ctx)
```

### Directory Operations

```go
dir := fs.File("s3://my-bucket/documents/")

// List files (non-recursive)
err := dir.ListDirInfo(ctx, func(info *fs.FileInfo) error {
    fmt.Println(info.Name, info.Size)
    return nil
}, nil)

// List with pattern matching
err = dir.ListDirInfo(ctx, callback, []string{"*.pdf", "*.doc"})

// Recursive listing
err = dir.ListDirInfoRecursive(ctx, callback, nil)
```

### Read-Only Mode

```go
// Create read-only filesystem to prevent accidental modifications
s3fs := s3fs.NewAndRegister(client, "my-bucket", true)
```

## Credentials

The package uses AWS SDK for Go V2 credential resolution. Credentials can be provided through:

1. **Environment variables**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`
2. **Shared credentials file**: `~/.aws/credentials`
3. **IAM roles** (when running on EC2, ECS, Lambda, etc.)
4. **Static credentials** (programmatically)

```go
import (
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/credentials"
)

// Static credentials example
client := s3.New(s3.Options{
    Region: "us-east-1",
    Credentials: credentials.NewStaticCredentialsProvider(
        "AKIAIOSFODNN7EXAMPLE",
        "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
        "",
    ),
})
```

See [AWS SDK for Go V2 Configuration](https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk/) for more details.

## S3-Compatible Services

This package works with any S3-compatible service:

### MinIO

```go
client := s3.New(s3.Options{
    Region: "us-east-1",
    Credentials: credentials.NewStaticCredentialsProvider(
        "minioadmin", "minioadmin", "",
    ),
    BaseEndpoint: aws.String("http://localhost:9000"),
    UsePathStyle: true,
})
```

### DigitalOcean Spaces

```go
client := s3.New(s3.Options{
    Region: "nyc3",
    Credentials: credentials.NewStaticCredentialsProvider(
        spacesKey, spacesSecret, "",
    ),
    BaseEndpoint: aws.String("https://nyc3.digitaloceanspaces.com"),
})
```

### Other S3-Compatible Services

Any service implementing the S3 API (Wasabi, Backblaze B2, etc.) can be used by configuring the appropriate endpoint.

## Implementation Details

### URL Format and AWS Compatibility

The package generates **standard AWS S3 URI format** (`s3://bucket-name/key`) for all operations:

```go
s3fs := s3fs.NewAndRegister(client, "my-bucket", false)

// URLs are AWS CLI/SDK compatible
s3fs.URL("/path/to/file.txt")     // Returns: s3://my-bucket/path/to/file.txt
s3fs.RootDir()                     // Returns: s3://my-bucket/
s3fs.Prefix()                      // Returns: s3://my-bucket
```

This format is compatible with:
- **AWS CLI**: `aws s3 cp s3://my-bucket/file.txt local.txt`
- **AWS SDKs**: Standard S3 URI format for all operations
- **Third-party tools**: Most tools that work with S3 recognize this format

**Note**: The `s3://` protocol is for programmatic access (AWS CLI/SDK). For web browser access, use presigned URLs or configure bucket policies with HTTPS URLs (`https://bucket.s3.region.amazonaws.com/key`).

### S3 Directories

S3 doesn't have true directories - they're simulated using object key prefixes. This package:

- Treats keys ending with `/` as directories
- Creates zero-byte objects with trailing `/` for `MakeDir()`
- Lists "directories" using S3 delimiter and common prefix features

### Touch Behavior

The `Touch()` method updates file modification times, but S3 doesn't support updating `LastModified` without rewriting the object. Therefore:

- For existing files: Copies the object to itself (updates `LastModified`)
- For new files: Creates an empty file

This is the only way to achieve "touch" behavior on S3.

### File Operations

- **Stat**: Uses `HeadObject` API, tries both with and without trailing `/` for directories
- **ReadAll**: Uses `GetObject` API, with multipart download for large files
- **WriteAll**: Uses `PutObject` API, with multipart upload for large files
- **Remove**: Uses `DeleteObject` API
- **CopyFile**: Uses `CopyObject` API (server-side copy, no download/upload)
- **ListDir**: Uses `ListObjectsV2` API with pagination support

## Testing

The package uses **Docker-based integration testing** with MinIO as an S3-compatible test server.

### Why Docker for Tests?

1. **Real S3 behavior**: Tests run against actual MinIO server, not mocks
2. **No embedded server**: MinIO doesn't provide an embeddable Go library
3. **Isolation**: Each test run gets a clean, isolated environment
4. **CI/CD friendly**: Works in automated pipelines without manual setup
5. **Production-like**: Tests validate against real S3 implementation

### Test Strategy

The test suite includes:

1. **Comprehensive filesystem tests** (`Test_fileSystem`):
   - Uses `fs.RunFileSystemTests` for standard compliance
   - Tests all file operations, directory operations, and metadata
   - 16+ sub-tests covering the entire interface

2. **Multipart transfer tests** (`Test_fileSystem_MultipartUploadDownload`):
   - Validates multipart upload/download with 11 MB files
   - Ensures data integrity across chunk transfers
   - Tests both `ReadAll` and `OpenReader` code paths

3. **Automatic Docker management**:
   - `TestMain` sets up MinIO container before tests
   - Creates test bucket automatically
   - Cleans up container after tests complete
   - Retries connection with exponential backoff

### Running Tests

```bash
# Requires Docker to be installed and running
go test -v

# Run specific test
go test -v -run Test_fileSystem

# Run with extended timeout for slow systems
go test -v -timeout 300s
```

### Test Requirements

- **Docker**: Must be installed and running
- **Network**: Needs to bind ports 9000-9001 for MinIO
- **Time**: Tests take ~4-5 seconds (including container startup)

If Docker is unavailable, tests are automatically skipped with a clear message.

## Performance Considerations

### Multipart Transfers

- **Upload**: Default part size is 5 MB, concurrent uploads improve throughput
- **Download**: Default concurrency is 5 goroutines, adjustable via `manager.Downloader`
- **Memory**: Uses `PartSize * Concurrency` memory during transfers

### Optimizations

1. **HeadObject caching**: Stat operations use `HeadObject` which is fast
2. **Paginated listing**: Directory listings use pagination to handle large directories
3. **Server-side copy**: `CopyFile` uses S3's server-side copy (no data transfer)
4. **Connection reuse**: S3 client maintains HTTP connection pool

## Limitations

1. **No atomic operations**: S3 doesn't support atomic rename or move
2. **Eventually consistent**: S3 is eventually consistent (though now strongly consistent for new objects)
3. **No append support**: Files must be rewritten entirely (no true append)
4. **Touch limitation**: Updating modification time requires copying object to itself
5. **No symbolic links**: S3 doesn't support symlinks
6. **No permissions**: S3 uses IAM policies, not POSIX permissions

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
