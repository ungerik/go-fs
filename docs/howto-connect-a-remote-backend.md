# How to connect a remote backend

Dial or configure one of the network backends and reach its files through the
normal `fs.File` API. The end result is the same for every backend: a file
system is registered for a URI prefix, and `fs.File("<prefix>/path")` works
from anywhere in your program.

## Prerequisites

- Credentials for the service you are connecting to
- The backend module in your `go.mod` — each network backend is a **separate Go
  module** with its own dependencies, so importing `s3fs` does not pull the
  Azure SDK into your build

```bash
go get github.com/ungerik/go-fs/s3fs   # or sftpfs, ftpfs, smbfs, webdavfs, azureblobfs, dropboxfs
```

## The constructor rule every backend follows

Every backend has the same two kinds of constructors, and sftpfs, ftpfs and
smbfs a third one:

```go
// New or Dial: returns the file system, never registers it
backendFS, err := backend.New(ctx, address, opts) // sftpfs, ftpfs and smbfs call it Dial
if err != nil {
    return err
}
defer backendFS.Close() // also unregisters it
err = fs.Register(backendFS)
if err != nil {
    return err
}

// NewAndRegister or DialAndRegister: registers it and returns only an error,
// for a file system that lives as long as the program
err = backend.NewAndRegister(ctx, address, opts)

// EnsureRegistered (sftpfs, ftpfs, smbfs): shares one connection per address
free, err := backend.EnsureRegistered(ctx, address, opts)
if err != nil {
    return err
}
defer free()

// Now any File with that prefix routes to it
data, err := fs.File("prefix://host/path/file.txt").ReadAll(ctx)
```

The arguments differ: `s3fs` takes a client and a bucket name, `azureblobfs` a
container client, `dropboxfs` an access token. What is always true:

1. **Register or you get nothing.** `New` and `Dial` create the file system
   without registering it; the `…AndRegister` variants also put it in the
   global registry, and `fs.Register` does it for the result of `New`. Only
   registered file systems are reachable through a `File` URI. An
   unregistered prefix resolves to the `Invalid` file system, not to a local
   path.
2. **`Close` unregisters.** After it, every `File` with that prefix errors.
   `defer Close()` right after `New` also cleans up when the registration
   fails; closing a file system that is not registered is fine.
3. **Constructors that talk to the server take a context** because dialing can
   take long. See [the context rule](explanation-context-rule.md).
4. **One file system per prefix.** `fs.Register` returns an error wrapping
   `fs.ErrAlreadyRegistered` if another file system holds the prefix. Use
   `EnsureRegistered` to share a connection instead of registering it twice.

`…AndRegister` exists where the prefix follows from the arguments, so that
the caller can use the URIs without the instance. To close such a file
system later, get it from the registry with
`fs.GetFileSystemByPrefixOrNil(prefix)`.

You can always work from the returned file system as well:

```go
err = backendFS.RootDir().Join("dir", "file.txt").WriteAllString(ctx, "...")
```

## Steps by backend

### S3

```go
import "github.com/ungerik/go-fs/s3fs"

// Default AWS credential chain (env, ~/.aws/config, IAM role, ...)
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", nil)
if err != nil {
    return err
}
defer bucket.Close()
err = fs.Register(bucket)
if err != nil {
    return err
}

err = fs.File("s3://my-bucket/path/file.txt").WriteAllString(ctx, "Hello")
```

The last argument is `*s3fs.Options`, nil for the defaults. With an existing
`aws-sdk-go-v2` client, registered for the lifetime of the program and
read-only:

```go
err := s3fs.NewAndRegister(client, "my-bucket", &s3fs.Options{ReadOnly: true})
```

The prefix is `s3://<bucket>`, so several buckets can be registered at once as
separate file systems. Buckets with the same name at different endpoints
need their own prefix, `Options.Prefix`. Uploads from
`Options.MultipartUploadThreshold` (16 MiB) on are multipart uploads,
`ReadAll` downloads objects from `Options.MultipartDownloadThreshold`
(16 MiB) on with concurrent range requests, and writers from `OpenWriter`
upload parts while they are written. Directories are key prefixes; `MakeDir`
writes a zero-byte marker object so empty directories exist.

Credential setup, S3-compatible services and the full concept mapping are in
[s3fs/README.md](../s3fs/README.md).

### SFTP

```go
import (
    "github.com/ungerik/go-fs/sftpfs"
    "golang.org/x/crypto/ssh/knownhosts"
)

// knownhosts.New returns the callback and an error, and it does not expand "~"
home, err := os.UserHomeDir()
if err != nil {
    return err
}
hostKeys, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
if err != nil {
    return err
}

sftpFS, err := sftpfs.Dial(ctx, "sftp://user@host", &sftpfs.Options{
    Password:        "secret",
    HostKeyCallback: hostKeys,
})
if err != nil {
    return err
}
defer sftpFS.Close()
err = fs.Register(sftpFS)
if err != nil {
    return err
}

data, err := fs.File("sftp://user@host/etc/hostname").ReadAll(ctx)
```

Port 22 is used when the address has none. The username and password come
from `Options.Username` and `Options.Password`, else from the address.
`Options.Credentials` takes precedence over both: a callback like
`sftpfs.Password`, `sftpfs.UsernameAndPassword` or your own
`sftpfs.CredentialsCallback`, which is called again before every reconnect, so
it can return rotated credentials. `Options.Auth` takes SSH authentication
methods like public keys, which are tried before the password.

**Host keys:** `Options.HostKeyCallback` is required, dialing fails without
it. `sftpfs.AcceptAnyHostKey` exists for tests and disables the check — do
not ship it.

A dial makes up to `1 + Options.ConnectRetries` attempts (3 retries by
default), each limited by `Options.Timeout`. Keep-alive requests detect a dead
connection, and the next operation reconnects. An operation that fails because
the connection was lost while it ran is retried once if repeating it can't
change the result: reads, `Truncate`, `SetPermissions`, `MakeAllDirs` and
`RemoveAll`. `Remove`, `Move`, `MakeDir`, `CreateSymbolicLink` and writers
return the connection error if their request may have reached the server.

To share one connection between independent callers, use reference counting
instead of dialing twice:

```go
free, err := sftpfs.EnsureRegistered(ctx, "sftp://user@host", &sftpfs.Options{
    Password:        "secret",
    HostKeyCallback: hostKeys,
})
if err != nil {
    return err
}
defer free() // closes when the last holder frees it
```

URIs with embedded credentials (`sftp://user:password@host`) can be passed to
`EnsureRegistered` as address. The password is used to dial, but it is not
part of the prefix, so the files are `sftp://user@host/path`.

### FTP / FTPS

```go
import "github.com/ungerik/go-fs/ftpfs"

ftpFS, err := ftpfs.Dial(ctx, "ftps://example.com", &ftpfs.Options{
    Username: "user",
    Password: "secret",
})
if err != nil {
    return err
}
defer ftpFS.Close()
err = fs.Register(ftpFS)
if err != nil {
    return err
}

data, err := fs.File("ftps://user@example.com/pub/notes.txt").ReadAll(ctx)
```

`ftp://` is plain FTP; `ftps://` is explicit TLS on port 21, or implicit TLS
with port 990. Server certificates are verified by default. The prefix is
`ftp://user@host` or `ftps://user@host`, with `:port` unless it is 21, or
`ftp://host` for an anonymous login.

```go
opts := &ftpfs.Options{
    Username:           "user",
    Password:           "secret",
    TLSConfig:          nil,   // nil verifies the server certificate for the dialed host
    InsecureSkipVerify: false, // true disables verification, also for a non-nil TLSConfig
    DebugOut:           nil,   // an io.Writer here logs the FTP protocol
}
```

`Options` also has `Credentials` for a callback, `Timeout`, `DialContext` for
the network connection, and `DisableEPSV` for servers that don't support
extended passive mode.

The single control connection serves one operation at a time; `OpenReader`
streams over its own connection. `ftpfs.EnsureRegistered` shares a connection
like the one of `sftpfs`.

### SMB / CIFS

```go
import "github.com/ungerik/go-fs/smbfs"

smbFS, err := smbfs.Dial(ctx, "smb://nas.local/documents", &smbfs.Options{
    Username: "alice",
    Password: "secret",
    Domain:   "WORKGROUP",
})
if err != nil {
    return err
}
defer smbFS.Close()
err = fs.Register(smbFS)
if err != nil {
    return err
}

files, err := fs.File("smb://alice@nas.local/documents/").ListDirMax(ctx, -1, "*.pdf")
```

The address is `[smb://][<user>[:<password>]@]<host>[:<port>]/<share>` with
the default port 445, and the prefix `smb://<user>@<host>/<share>`, or
`smb://<host>/<share>` without a user. `Options.Username` and `Options.Password`
override credentials in the address; `Domain` and `Workstation` are optional
NTLMv2 parameters, and `DialContext` opens the network connection.
`smbfs.DialAndRegister` and `smbfs.EnsureRegistered` follow the constructor
rule above.

SMB gives real directories and random access handles, so nearly every optional
interface is native. Permissions map to the SMB read-only attribute, so only
the user write bit survives a round trip.

### WebDAV

```go
import "github.com/ungerik/go-fs/webdavfs"

davFS, err := webdavfs.New(ctx,
    "https://cloud.example.com/remote.php/dav/files/alice",
    &webdavfs.Options{Username: "alice", Password: "app-password"})
if err != nil {
    return err
}
defer davFS.Close()
err = fs.Register(davFS)
if err != nil {
    return err
}

notes, err := fs.File("webdav://alice@cloud.example.com/remote.php/dav/files/alice/notes.txt").
    ReadAllString(ctx)
```

Note the prefix is `webdav://` even though the base URL is `https://`, and it
contains the username: `webdav://<user>@<host><base path>`, or
`webdav://<host><base path>` without one. Paths map to URL paths below the
base URL.

```go
opts := &webdavfs.Options{
    Header: http.Header{"Authorization": {"Bearer " + token}}, // for token auth
    Client: nil, // nil uses http.DefaultClient
}
```

`Username` and `Password` are sent with basic authentication. An
`Authorization` header in `Options.Header` takes precedence: then no basic
authentication is sent, and a username only becomes part of the prefix.

Standard library only, so one module covers every WebDAV server. `PROPFIND`
backs `Stat` and `ListDir`, `MOVE` and `COPY` are native, and readers seek with
`Range` requests.

### Azure Blob Storage

```go
import "github.com/ungerik/go-fs/azureblobfs"

blobFS, err := azureblobfs.NewFromConnectionString(ctx,
    os.Getenv("AZURE_STORAGE_CONNECTION_STRING"), "assets", nil)
if err != nil {
    return err
}
defer blobFS.Close()
err = fs.Register(blobFS)
if err != nil {
    return err
}

err = blobFS.RootDir().Join("images", "logo.png").WriteAll(ctx, logo)
```

For other credential types, build a `container.Client` yourself and pass it to
`azureblobfs.New(ctx, client, opts)` or `azureblobfs.NewAndRegister`. `New`
lists one blob to check the container and the credentials;
`Options.SkipContainerCheck` skips that for credentials without the list
permission, and `Options.ReadOnly` rejects writes. The prefix is
`azblob://<host>/<container>`. Directories are blob name prefixes with marker
blobs like s3fs, `CopyFile` is a server-side copy, and `Touch` updates the
modification time by setting blob metadata.

### Dropbox

```go
import "github.com/ungerik/go-fs/dropboxfs"

dbxFS, err := dropboxfs.New(ctx, accessToken, &dropboxfs.Options{CacheTimeout: 5 * time.Minute})
if err != nil {
    return err
}
defer dbxFS.Close()
err = fs.Register(dbxFS)
if err != nil {
    return err
}

err = dbxFS.RootDir().Join("Apps", "MyApp", "notes.md").WriteAllString(ctx, "...")
```

`Options.CacheTimeout` is the metadata cache timeout (zero disables the
cache); `Options.Mute` mutes the notifications Dropbox sends for changed
files. Dropbox access tokens are short-lived, so a long running program
passes `Options.HTTPClient`, a client that refreshes them, like the client of
a `golang.org/x/oauth2` config; the access token argument is ignored then.

The prefix is discovered at construction: `dropbox://` followed by the
account id without its `dbid:` part (`ID()` returns the account id with
`dbid:`) — so build paths from `RootDir()` rather than hardcoding the URI. That is also
why `New` and `fs.Register` are the usual pair; `dropboxfs.NewAndRegister` is
for callers that know the account id.

### HTTP (read-only)

No dialing, no credentials, just a side-effect import:

```go
import _ "github.com/ungerik/go-fs/httpfs"

data, err := fs.File("https://example.com/file.txt").ReadAll(ctx)
```

It registers `httpfs.Default` and `httpfs.DefaultTLS` for `http://` and
`https://`, using `http.DefaultClient`. To control timeouts, proxies or
authentication, register a file system with its own client for a more
specific prefix; URLs with that prefix then use it:

```go
err := httpfs.NewAndRegister("https://api.example.com/", &httpfs.Options{
    Client: &http.Client{Timeout: 30 * time.Second},
})
```

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

**`invalid file system: no file system registered for "…"`** — nothing is
registered for that prefix, so the URI resolved to the `Invalid` file system.
You used `New` or `Dial` without `fs.Register`, forgot the import, already
closed it, or the URI doesn't match the prefix: the prefix of `sftpfs`,
`ftpfs`, `smbfs` and `webdavfs` contains the username if one is set,
`sftp://user@host` and not
`sftp://host`. Check with `errors.Is(err, fs.ErrInvalidFileSystem)`, and list
the prefixes with `fs.RegisteredFileSystems()`.

**`file system prefix already registered`** — another file system holds the
prefix. Close the first one, give the second one another prefix where the
backend has an option for it (`s3fs.Options.Prefix`), or share the first one
with `EnsureRegistered`.

**A file lands on local disk instead of the remote** — only possible for a
scheme-less path. Any string containing `://` that matches no registered file
system resolves to `Invalid`, never to local.

**`errors.Is(err, errors.ErrUnsupported)`** — the backend cannot do that
operation and there is no emulation for it. Check the support matrix in
[FileSystem interfaces](reference-filesystem-interfaces.md).

**Random access on an object store is slow** — `OpenReadWriter` buffers the
whole object in memory and writes it back on `Close`. That is the documented
fallback for backends without native support, and the native `OpenReadWriter`
of `s3fs`, `azureblobfs` and `dropboxfs` does the same: it downloads the whole
object when opened and uploads it again on `Close`, because these stores can't
change an object in place. Use `ReadAll`/`WriteAll` instead if the object is
large.

**SFTP host key errors** — the server key is not in your `known_hosts`. Add it,
or supply the callback you actually want. Do not reach for
`sftpfs.AcceptAnyHostKey` outside tests.

**Everything errors with `file system is closed`** — `Close` was called,
possibly by a `defer` in a function that already returned. When several
components share a connection, use `EnsureRegistered` of `sftpfs`, `ftpfs` or
`smbfs` and their `free` functions instead of `Close`.

## Related

- [Getting started](tutorial-getting-started.md)
- [Paths, URIs and the registry](explanation-path-and-uri-model.md) — how a prefix resolves
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the per-backend support matrix
- [Errors](reference-errors.md)
- [s3fs/README.md](../s3fs/README.md) — S3 credentials and concept mapping
