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
    data, err := file.ReadAll(ctx)        // GET request
    reader, err := file.OpenReader()      // streams the GET body, for large files
}
```

`httpfs.Default` and `httpfs.DefaultTLS` are the two registered file systems,
they use `http.DefaultClient`. To use another `*http.Client`, with timeouts,
a proxy or authentication, register a file system for a more specific prefix;
URLs with that prefix then use it instead of `DefaultTLS`:

```go
err := httpfs.NewAndRegister("https://api.example.com/", &httpfs.Options{
    Client: &http.Client{Timeout: 30 * time.Second},
})
```

`httpfs.New` returns such a file system without registering it.

## How HTTP concepts map to the file system

- **URLs are used verbatim.** The path of a file is its URL in escaped form
  without the prefix: for `Default` and `DefaultTLS` it starts with the host,
  like `example.com/data/report.pdf?version=2`, for a file system registered
  for `https://api.example.com/` it is relative to that prefix. The query and
  the fragment are never decoded or cleaned, so `?q=a%26b`,
  `?to=https://example.com//x` and the signature of a presigned S3 URL reach
  the server unchanged. Escapes in the path like `%2F` or `%3F` stay escaped,
  a trailing slash is kept, only `.` and `..` elements and duplicate slashes
  are cleaned.
- **The host is a volume.** No `..` leads to another host:
  `fs.File("https://example.com/files").Join("..", "..", "evil.example")` is
  `https://example.com/evil.example`.
- **Names.** `Name` is the last path element in its escaped form, without the
  query and fragment (`report%20final.pdf`; decode it with `url.PathUnescape`
  if needed). The URL of a host without path has an empty `Name` and is its own
  `Dir`. `Dir` and `Join` drop the query and fragment, and the joined parts
  are path elements, so a `?` in them is escaped.
- **Metadata.** `Stat` sends a `HEAD` request. If the server doesn't report
  a `Content-Length` or doesn't support `HEAD`, a `GET` for the first byte
  (`Range: bytes=0-0`) follows and the size is taken from its
  `Content-Range`, so the content is never downloaded just to get its size.
  `Size` is -1 if the server reports no size at all, `Modified` comes from
  `Last-Modified`.
- **Reading.** `OpenReader` streams the response body of a `GET` without
  buffering the whole file in memory; `ReadAll` is native and allocates
  its buffer once for the `Content-Length`, up to 16 MiB.
- **No directories.** HTTP has no directory listing, so `ListDir` returns
  an error wrapping `errors.ErrUnsupported`.
- **Read-only.** Every write operation returns `fs.ErrReadOnlyFileSystem`.
- **Errors.** 404 and 410 wrap `os.ErrNotExist`. Other error statuses and
  transport failures are errors that don't, because the existence of the URL
  is unknown then. The messages of status and transport errors omit the
  query and the password of the URL.

## Testing

The tests run against an in-process `httptest` server, so they need no
network access.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
