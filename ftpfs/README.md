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

    free, err := ftpfs.EnsureRegistered(
        ctx,
        "ftps://user@example.com",            // [ftp[s]://][user[:password]@]host[:port]
        &ftpfs.Options{Password: "secret"}, // nil uses the defaults
    )
    if err != nil {
        panic(err)
    }
    defer free()

    // Use it like any other go-fs file system
    file := fs.File("ftps://user@example.com/pub/notes.txt")

    err = file.WriteAllString(ctx, "Hello, FTP!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

- `Dial(ctx, address, opts)` returns a file system without registering it.
- `DialAndRegister(ctx, address, opts)` registers it so `fs.File` values
  with its prefix work, and returns only an error. It fails with
  `fs.ErrAlreadyRegistered` if the prefix is taken.
- `EnsureRegistered(ctx, address, opts)` shares one file system per URI
  prefix between callers with reference counting and returns a `free`
  function instead of `Close`.

File URIs have the form `ftp://user@host:port/path` or
`ftps://user@host:port/path`. The port is omitted if it is 21, and
`ftp://user@host:21/path` is accepted as well. `ftpFS.ID()` is the URI
prefix of the connection (`ftp://user@host`). URIs, `FileInfo.File`,
error messages and `String()` never contain the password. There is no file
system for the bare `ftp://` and `ftps://` prefixes, so a URI with a
password like `ftp://user:secret@host/path` doesn't resolve: dial the
address `ftp://user:secret@host` and use URIs without the password.

### Credentials

`Options.Username` and `Options.Password` override the user info of the
address. `Options.Credentials` is a `CredentialsCallback` that is called
instead for the first dial and again for every reconnect; `ftpfs.Password`
and `ftpfs.UsernameAndPassword` are simple callbacks. Without a username
the login is anonymous as defined for `ftp://` URLs by RFC 1738: the user
`anonymous` with `Options.Password` (customarily an e-mail address) or
`anonymous`, and the URI prefix has no user (`ftp://example.com`).

Paths and credentials with CR, LF or NUL characters are rejected with an
error wrapping `os.ErrInvalid`, because they would inject FTP commands.

### TLS

`ftp://` is plain FTP. `ftps://` uses explicit TLS (`AUTH TLS`) on port 21
by default and implicit TLS when the port is 990. The server certificate is
verified for the dialed host unless `Options.InsecureSkipVerify` is set.
The data connections resume the TLS session of the control connection,
which servers like vsftpd (`require_ssl_reuse`) and FileZilla Server
require; a `ClientSessionCache` is added to a `TLSConfig` without one.

```go
ftpFS, err := ftpfs.Dial(ctx, "ftps://user@example.com", &ftpfs.Options{
    Password:    "secret",
    TLSConfig:   &tls.Config{MinVersion: tls.VersionTLS13},
    Timeout:     10 * time.Second, // dial, TLS handshake, greeting and login
    DialContext: proxyDialer.DialContext,
    DebugOut:    os.Stderr, // FTP protocol log, the password is replaced by ****
})
```

### Data connections

Data connections use EPSV (RFC 2428) and fall back to PASV if the server
rejects it; `Options.DisableEPSV` uses PASV right away. Either way they go
to the host of the control connection with the port of the server's reply,
never to the address of a PASV reply, which could point to another or an
internal host.

### Connections, retries and cancellation

A dialed file system keeps one control connection that is used by one
operation at a time. If it breaks, the next operation reconnects. An
operation that failed because the connection broke is retried once on a new
connection, but only if nothing was changed on the server yet: after an
`APPE`, `DELE`, `MKD`, `RMD` or `RNTO` command was sent, the operation
returns the connection error ("may have been executed") instead of running
the command twice, because only its reply may have been lost. Before such
a command, an idle connection is checked with `NOOP`, so a connection that
the server closed while it was idle is replaced first.

`OpenReader` dials a dedicated connection for the transfer, so a streaming
read never blocks other operations.

Operations with a `context.Context` are aborted when the context is done.
`Close` unregisters the file system, aborts running operations, reconnects
and open readers, and afterwards every method returns
`fs.ErrFileSystemClosed`.

## How FTP concepts map to the file system

- **Paths.** The server's paths, separator `/`. Listings are parsed by the
  FTP client library, so what a server reports about modification times and
  sizes is what the `FileInfo` contains.
- **Native operations.** `ReadAll`, `WriteAll`, `Append`,
  `OpenAppendWriter`, `OpenReadWriter`, `Touch`, `RemoveAll`,
  `ListDirRecursive` and `Move` are native; everything else uses the generic
  emulation of the `fs` package. Writers buffer the data in memory and
  store it with one `STOR` or `APPE` in `Close`; they implement
  `fs.WriteAborter`.
- **Move.** Servers differ in what a rename onto an existing path does, so
  `Move` checks the destination first: an existing directory is an error
  wrapping `os.ErrExist`, an existing file is replaced, a directory can't
  replace a file (`fs.ErrIsNotDirectory`), and a directory can't be moved
  into itself (`os.ErrInvalid`).
- **Permissions.** FTP has no portable permission model. The reported
  permissions are the constants `ftpfs.DefaultPermissions` /
  `ftpfs.DefaultDirPermissions`, and `SetPermissions` is not supported.
- **Errors.** Missing files wrap `os.ErrNotExist`, reading or writing a
  directory is an `fs.ErrIsDirectory`, `MakeDir` on an existing path wraps
  `os.ErrExist`, removing a non-empty directory is an
  `fs.ErrDirectoryNotEmpty`, and after `Close` every method returns
  `fs.ErrFileSystemClosed`.

## Testing

The tests, including the go-fs conformance suite over FTP, over FTPS with
TLS session reuse and against a server without MLST, run against an
in-process FTP server with failure hooks, so they need neither Docker nor a
network.

Tests against public internet FTP servers only run with
`GOFS_ONLINE_TESTS=1`.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
