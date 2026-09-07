# FTP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for FTP and FTPS
servers, built on [github.com/jlaffaye/ftp](https://github.com/jlaffaye/ftp).

## Installation

```bash
go get github.com/ungerik/go-fs/ftpfs
```

## Usage

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/ftpfs"
)

func main() {
    ctx := context.Background()

    ftpFS, err := ftpfs.DialAndRegister(
        ctx,
        "ftps://example.com",                        // ftp:// or ftps://
        ftpfs.UsernameAndPassword("user", "secret"), // or Password for anonymous-style logins
        nil,                                         // *ftpfs.Options, nil uses the defaults
    )
    if err != nil {
        panic(err)
    }
    defer ftpFS.Close()

    // Use it like any other go-fs file system
    file := fs.File("ftps://user@example.com/pub/notes.txt")

    err = file.WriteAllString(ctx, "Hello, FTP!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

`Dial` returns a file system without registering it, `DialAndRegister`
registers it so `fs.File` values with its prefix work, and
`EnsureRegistered` shares one connection per address between callers with
reference counting and returns a `free` function instead of `Close`.

File URIs have the form `ftp://user@host/path` or `ftps://user@host/path`.
`ftpFS.ID()` is the URI prefix of the connection (`ftp://user@host`).

### TLS

`ftp://` is plain FTP. `ftps://` uses explicit TLS (`AUTH TLS`) on port 21
by default and implicit TLS when the port is 990. The server certificate is
verified for the dialed host unless `Options.InsecureSkipVerify` is set:

```go
ftpFS, err := ftpfs.DialAndRegister(ctx, "ftps://example.com",
    ftpfs.UsernameAndPassword("user", "secret"),
    &ftpfs.Options{
        TLSConfig:          &tls.Config{MinVersion: tls.VersionTLS12},
        InsecureSkipVerify: false,
        DebugOut:           os.Stderr, // FTP protocol log
    },
)
```

### Connections

A dialed file system keeps one control connection that is used by one
operation at a time. If it breaks, the next operation reconnects with the
stored credentials and is retried once. `OpenReader` dials a dedicated
connection for the transfer, so a streaming read never blocks other
operations.

URIs with embedded credentials (`ftp://user:password@host/path`) are served
by the file systems registered for the plain `ftp://` and `ftps://`
prefixes, which dial a connection per operation.

## How FTP concepts map to the file system

- **Paths.** The server's paths, separator `/`. Listings are parsed by the
  FTP client library, so what a server reports about modification times and
  sizes is what the `FileInfo` contains.
- **Native operations.** `ReadAll`, `WriteAll`, `Append`,
  `OpenAppendWriter`, `OpenReadWriter`, `Touch`, `RemoveAll`,
  `ListDirRecursive` and `Move` are native; everything else uses the generic
  emulation of the `fs` package.
- **Permissions.** FTP has no portable permission model. The reported
  permissions are `ftpfs.DefaultPermissions` /
  `ftpfs.DefaultDirPermissions`, and `SetPermissions` is not supported.
- **Errors.** Missing files wrap `os.ErrNotExist`, `MakeDir` on an existing
  path wraps `os.ErrExist`, and after `Close` every method returns
  `fs.ErrFileSystemClosed`.

## Testing

The tests, including the go-fs conformance suite, run against an in-process
FTP server, so they need neither Docker nor a network.

Tests against public internet FTP servers only run with
`GOFS_ONLINE_TESTS=1`.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
