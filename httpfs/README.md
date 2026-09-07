# HTTP File System

A read-only [go-fs](https://github.com/ungerik/go-fs) file system for HTTP
URLs, so a remote file can be passed to any code that takes an `fs.File` or
`fs.FileReader`.

This package is part of the `github.com/ungerik/go-fs` module, no separate
`go get` is needed.

## Usage

Importing the package registers the file systems for the `http://` and
`https://` prefixes, so any HTTP URL is a `fs.File`:

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    _ "github.com/ungerik/go-fs/httpfs" // registers http:// and https://
)

func main() {
    ctx := context.Background()

    file := fs.File("https://example.com/data/report.pdf")

    name := file.Name()                   // "report.pdf"
    size := file.Size()                   // from the Content-Length header
    exists := file.Exists()               // HEAD request
    data, err := file.ReadAll(ctx)        // streams the GET body
    reader, err := file.OpenReader()      // for large files
}
```

The exported `httpfs.FileSystem` and `httpfs.FileSystemTLS` are the two
registered file systems; `httpfs.Client` is the `*http.Client` used for all
requests and can be replaced to configure timeouts, proxies or transports:

```go
httpfs.Client = &http.Client{Timeout: 30 * time.Second}
```

## How HTTP concepts map to the file system

- **Paths.** The whole URL is the `fs.File`, so the path is the URL path.
  `Name` is the last path element, without the query string.
- **Metadata.** `Stat` sends a `HEAD` request and falls back to a `GET` if
  the server does not answer `HEAD` or does not report a `Content-Length`.
  `Size` comes from `Content-Length`, `Modified` from `Last-Modified`.
- **Reading.** `OpenReader` streams the response body of a `GET` without
  buffering the whole file in memory; `ReadAll` is native.
- **No directories.** HTTP has no directory listing, so `ListDir` returns
  an error wrapping `errors.ErrUnsupported`.
- **Read-only.** Every write operation returns `fs.ErrReadOnlyFileSystem`.
- **Errors.** 404 and other not-found statuses wrap `os.ErrNotExist`.

## Testing

The tests run against an in-process `httptest` server, so they need no
network access.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
