# How to connect a remote backend

Dial or configure one of the network backends and reach its files through the
normal `fs.File` API. The end result is the same for every backend: a URI
prefix is registered, and `fs.File("<prefix>/path")` works from anywhere in
your program.

## Prerequisites

- Credentials for the service you are connecting to
- The backend module in your `go.mod` — each network backend is a **separate Go
  module** with its own dependencies, so importing `s3fs` does not pull the
  Azure SDK into your build

```bash
go get github.com/ungerik/go-fs/s3fs   # or sftpfs, ftpfs, smbfs, webdavfs, azureblobfs, dropboxfs
```

## The shape every backend shares

```go
backendFS, err := backend.DialAndRegister(ctx, address, credentials, options)
if err != nil {
    return err
}
defer backendFS.Close()

// Now any File with that prefix routes to it
data, err := fs.File("prefix://host/path/file.txt").ReadAll(ctx)
```

Three things are always true:

1. **Register or you get nothing.** A constructor named `New`/`Dial` creates
   the file system without registering it; `NewAndRegister`/`DialAndRegister`
   also puts it in the global registry. Only registered file systems are
   reachable through a `File` URI. An unregistered prefix resolves to the
   `Invalid` file system, not to a local path.
2. **`Close` unregisters.** After it, every `File` with that prefix errors.
3. **Constructors take a context** because dialing can take long. See
   [the context rule](explanation-context-rule.md).

You can always skip the registry and work from the returned file system:

```go
err = backendFS.RootDir().Join("dir", "file.txt").WriteAllString(ctx, "...")
```

## Steps by backend

### S3

```go
import "github.com/ungerik/go-fs/s3fs"

// Default AWS credential chain (env, ~/.aws/config, IAM role, ...)
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", false)
if err != nil {
    return err
}
defer bucket.Close()

err = fs.File("s3://my-bucket/path/file.txt").WriteAllString(ctx, "Hello")
```

The last argument is `readOnly`. With an existing `aws-sdk-go-v2` client:

```go
bucket := s3fs.NewAndRegister(client, "my-bucket", false)
```

The prefix is `s3://<bucket>`, so several buckets can be registered at once as
separate file systems. Multipart upload and download kick in automatically
above `s3fs.MultipartUploadThreshold` (5 MB) and
`s3fs.MultipartDownloadThreshold` (10 MB). Directories are key prefixes;
`MakeDir` writes a zero-byte marker object so empty directories exist.

Credential setup, S3-compatible services and the full concept mapping are in
[s3fs/README.md](../s3fs/README.md).

### SFTP

```go
import (
    "github.com/ungerik/go-fs/sftpfs"
    "golang.org/x/crypto/ssh/knownhosts"
)

hostKey, err := knownhosts.New(os.ExpandEnv("$HOME/.ssh/known_hosts"))
if err != nil {
    return err
}

sftpFS, err := sftpfs.DialAndRegister(ctx,
    "sftp://user@host",
    sftpfs.Password("secret"),
    hostKey,
    nil, // optional fs.Logger for connection events
)
if err != nil {
    return err
}
defer sftpFS.Close()

data, err := fs.File("sftp://user@host/etc/hostname").ReadAll(ctx)
```

Port 22 is used when the address has none. Credentials come from
`sftpfs.Password`, `sftpfs.UsernameAndPassword`, or your own
`sftpfs.CredentialsCallback`.

**Host keys:** pass a real `ssh.HostKeyCallback`. `sftpfs.AcceptAnyHostKey`
exists for tests and disables the check — do not ship it.

A lost connection is re-dialed transparently, up to
`sftpfs.MaxConnectRetries` (3).

To share one connection between independent callers, use reference counting
instead of dialing twice:

```go
free, err := sftpfs.EnsureRegistered(ctx, "sftp://user@host", cred, hostKey, nil)
if err != nil {
    return err
}
defer free() // closes when the last holder frees it
```

URIs with embedded credentials (`sftp://user:password@host/path`) dial a
connection per operation and require `sftpfs.URLHostKeyCallback` to be set.

### FTP / FTPS

```go
import "github.com/ungerik/go-fs/ftpfs"

ftpFS, err := ftpfs.DialAndRegister(ctx,
    "ftps://example.com",
    ftpfs.UsernameAndPassword("user", "secret"),
    nil, // *ftpfs.Options
)
if err != nil {
    return err
}
defer ftpFS.Close()
```

`ftp://` is plain FTP; `ftps://` is explicit TLS on port 21, or implicit TLS
with port 990. Server certificates are verified by default.

```go
&ftpfs.Options{
    TLSConfig:          nil,   // nil verifies the server certificate for the dialed host
    InsecureSkipVerify: false, // true disables verification, also for a non-nil TLSConfig
    DebugOut:           nil,   // an io.Writer here logs the FTP protocol
}
```

The single control connection serves one operation at a time; `OpenReader`
streams over its own connection.

### SMB / CIFS

```go
import "github.com/ungerik/go-fs/smbfs"

smbFS, err := smbfs.DialAndRegister(ctx, "smb://alice@nas.local/documents",
    &smbfs.Options{Password: "secret", Domain: "WORKGROUP"})
if err != nil {
    return err
}
defer smbFS.Close()

files, err := fs.File("smb://alice@nas.local/documents/").ListDirMax(ctx, -1, "*.pdf")
```

The address is `smb://<user>@<host>/<share>`. `Options.Username` and
`Options.Password` override credentials in the address; `Domain` and
`Workstation` are optional NTLMv2 parameters.

SMB gives real directories and random access handles, so nearly every optional
interface is native. Permissions map to the SMB read-only attribute, so only
the user write bit survives a round trip.

### WebDAV

```go
import "github.com/ungerik/go-fs/webdavfs"

davFS, err := webdavfs.NewAndRegister(ctx,
    "https://cloud.example.com/remote.php/dav/files/alice",
    &webdavfs.Options{Username: "alice", Password: "app-password"})
if err != nil {
    return err
}
defer davFS.Close()

notes, err := fs.File("webdav://cloud.example.com/remote.php/dav/files/alice/notes.txt").
    ReadAllString(ctx)
```

Note the prefix is `webdav://` even though the base URL is `https://`. Paths
map to URL paths below the base URL.

```go
&webdavfs.Options{
    Username: "alice",
    Password: "app-password",
    Header:   http.Header{"Authorization": {"Bearer " + token}}, // for token auth
    Client:   nil, // nil uses http.DefaultClient
}
```

Standard library only, so one module covers every WebDAV server. `PROPFIND`
backs `Stat` and `ListDir`, `MOVE` and `COPY` are native, and readers seek with
`Range` requests.

### Azure Blob Storage

```go
import "github.com/ungerik/go-fs/azureblobfs"

blobFS, err := azureblobfs.NewFromConnectionString(ctx,
    os.Getenv("AZURE_STORAGE_CONNECTION_STRING"), "assets", false)
if err != nil {
    return err
}
defer blobFS.Close()

err = blobFS.RootDir().Join("images", "logo.png").WriteAll(ctx, logo)
```

For other credential types, build a `container.Client` yourself and pass it to
`azureblobfs.NewAndRegister(ctx, client, readOnly)`. The prefix is
`azblob://<host>/<container>`. Directories are blob name prefixes with marker
blobs like s3fs, `CopyFile` is a server-side copy, and `Touch` updates the
modification time by setting blob metadata.

### Dropbox

```go
import "github.com/ungerik/go-fs/dropboxfs"

dbxFS, err := dropboxfs.NewAndRegister(ctx, accessToken, 5*time.Minute, false)
if err != nil {
    return err
}
defer dbxFS.Close()

err = dbxFS.RootDir().Join("Apps", "MyApp", "notes.md").WriteAllString(ctx, "...")
```

The second argument is the metadata cache timeout (zero disables the cache);
the third mutes the notifications Dropbox sends for changed files.

The prefix is `dropbox://<account id>`, which is discovered at construction —
so build paths from `RootDir()` rather than hardcoding the URI.

### HTTP (read-only)

No dialing, no credentials, just a side-effect import:

```go
import _ "github.com/ungerik/go-fs/httpfs"

data, err := fs.File("https://example.com/file.txt").ReadAll(ctx)
```

It registers both `http://` and `https://`. Set `httpfs.Client` to a
configured `*http.Client` to control timeouts, proxies or transports.

## Verification

Read something back through the URI, not through the returned file system —
that proves registration worked:

```go
files, err := fs.File("<prefix>/").ListDirMax(ctx, 10)
if err != nil {
    return err
}
for _, f := range files {
    fmt.Println(f.Name(), f.Size())
}
```

Confirm the file system is registered and see its stable id:

```go
for _, f := range fs.RegisteredFileSystems() {
    fmt.Printf("%s → %s (id %s)\n", f.Prefix(), f.Name(), f.ID())
}
```

## Troubleshooting

**Reads return `file system is write-only`, writes return `file system is
read-only`** — nothing is registered for that prefix, so the URI resolved to
the `Invalid` file system. You used a `New`/`Dial` constructor instead of the
`AndRegister` variant, forgot the import, or already closed it. Check with
`fs.File(uri).FileSystem()`.

**A file lands on local disk instead of the remote** — only possible for a
scheme-less path. Any string containing `://` that matches no registered file
system resolves to `Invalid`, never to local.

**`errors.Is(err, errors.ErrUnsupported)`** — the backend cannot do that
operation and there is no emulation for it. Check the support matrix in
[FileSystem interfaces](reference-filesystem-interfaces.md).

**Random access on an object store is slow** — `OpenReadWriter` on a backend
without native support buffers the whole object in memory and writes it back on
`Close`. That is the documented fallback; use `ReadAll`/`WriteAll` instead if
the object is large.

**SFTP host key errors** — the server key is not in your `known_hosts`. Add it,
or supply the callback you actually want. Do not reach for
`sftpfs.AcceptAnyHostKey` outside tests.

**Everything errors with `file system is closed`** — `Close` was called,
possibly by a `defer` in a function that already returned. When several
components share a connection, use `sftpfs.EnsureRegistered` / `ftpfs.EnsureRegistered`
and their `free` functions instead of `Close`.

## Related

- [Getting started](tutorial-getting-started.md)
- [Paths, URIs and the registry](explanation-path-and-uri-model.md) — how a prefix resolves
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the per-backend support matrix
- [Errors](reference-errors.md)
- [s3fs/README.md](../s3fs/README.md) — S3 credentials and concept mapping
