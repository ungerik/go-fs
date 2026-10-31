# WebDAV File System

A [go-fs](https://github.com/ungerik/go-fs) file system for WebDAV servers:
Nextcloud, ownCloud, Apache `mod_dav`, nginx, SharePoint or the
`golang.org/x/net/webdav` handler.

The client is implemented with the standard library only, so one module
covers every server.

## Installation

Get the backend together with the go-fs core module, at the same version:

```bash
go get github.com/ungerik/go-fs@v1.0.0-rc.2 github.com/ungerik/go-fs/webdavfs@v1.0.0-rc.2
```

## Usage

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/webdavfs"
)

func main() {
    ctx := context.Background()

    davFS, err := webdavfs.New(
        ctx,
        "https://cloud.example.com/remote.php/dav/files/alice",
        &webdavfs.Options{Username: "alice", Password: "app-password"},
    )
    if err != nil {
        panic(err)
    }
    // Register it so that fs.File values with its prefix work
    if err := fs.Register(davFS); err != nil {
        panic(err)
    }
    defer davFS.Close() // also unregisters it

    // Use it like any other go-fs file system
    file := davFS.RootDir().Join("Documents", "notes.txt")

    err = file.WriteAllString(ctx, "Hello, WebDAV!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

`New` returns a file system without registering it. `NewAndRegister`
registers it and returns only an error, for callers that work with file
URIs; `fs.GetFileSystem(uri)` returns the registered file system.

File URIs have the form `webdav://<user>@<host><base path>/<path>`, so the
example above is reachable as
`webdav://alice@cloud.example.com/remote.php/dav/files/alice/Documents/notes.txt`.
The username is part of the prefix because endpoints like Nextcloud's
`/remote.php/webdav` show every user their own files under the same URL, so
the same server can be registered once per user. Without a username the
form is `webdav://<host><base path>/<path>`. The password never appears in
the prefix, in `String()` or in error messages. `davFS.ID()` is the prefix
without the scheme.

A `/`, `?` or `#` in the username or password of the base URL must be
percent-encoded (`%2F`, `%3F`, `%23`). An `@` in the path of a base URL
without user info whose host has a port must be percent-encoded too (`%40`),
like `https://cloud.example.com:8443/remote.php/dav/files/alice%40example.com`,
else `New` rejects the URL: `url.Parse` would read a username with a password
of digits and an unencoded `/`, like `alice:1234/5678@host`, as the host
`alice` with the port `1234`. A base URL with a password and an unencoded
`@` after the host is rejected with an error wrapping `os.ErrInvalid`, because
`url.Parse` would read a password with an `@` followed by a `/`, like
`s3cr@et/Pw` of `https://alice:s3cr@et/Pw@host/dav`, as the host `et` and the
path `/Pw@host/dav`. Pass the password with `Options.Password` and the
username with `Options.Username` then. A base URL with a username only may have an `@` in
the path, like
`https://alice@cloud.example.com/remote.php/dav/files/alice@example.com`.

### Authentication

`Options.Username` and `Options.Password` are sent as HTTP basic auth and
override the credentials embedded in the base URL; they are used only
together, `Options.Password` without `Options.Username` is ignored.
`Options.Header` is added to every request, for bearer tokens for example. An
`Authorization` header in `Options.Header` takes precedence over basic auth:
the username and password are not sent then, a username only becomes part of
the prefix. `Options.Client` replaces the `*http.Client` used for all
requests:

```go
&webdavfs.Options{
    Header: http.Header{"Authorization": {"Bearer " + token}},
    Client: &http.Client{Timeout: 30 * time.Second},
}
```

The file system uses a copy of `Options.Client` with its own
`CheckRedirect`, see below.

## How WebDAV concepts map to the file system

- **Paths.** File system paths map directly to URL paths below the base URL. A
  path with a backslash is an error wrapping `os.ErrInvalid` unless
  `Options.AllowBackslashInNames` is set: for the separator `/` the backslash
  is part of a name, but a server on Windows (IIS) takes it for a separator
  and resolves `..`, so a name like `..\x` from a `SubFileSystem` or
  `CopyRecursive` would reach a file outside of its directory. Set the option
  only for servers that store backslashes in names, like servers on Unix.
  The file system implements `fs.ValidateNameFileSystem` with this rule and
  rejects new names with a NUL character or that are not valid UTF-8, which
  servers that store the files on disk can't write, so a copy or move into
  it fails before it writes anything.
- **Methods.** `PROPFIND` backs `Stat` and `ListDir`, `GET` backs reads,
  `PUT` backs writes, `MKCOL` backs `MakeDir`, `DELETE` backs `Remove`, and
  `MOVE` and `COPY` back `Move` and `CopyFile`.
- **Native operations.** `ReadAll`, `WriteAll`, `CopyFile`, `Move` and
  `RemoveAll` are native, and readers seek with `Range` requests; everything
  else uses the generic emulation of the `fs` package.
- **Writers.** `OpenWriter` buffers the file in memory and uploads it with
  one `PUT` on `Close`. The writer implements `fs.WriteAborter`: `Abort`
  discards the data, so a failed copy never replaces a file with partial
  content. Uploads are not streamed, because servers store the body of an
  interrupted `PUT` as the file.
- **Move and copy.** `MOVE` and `COPY` are sent with `Overwrite: F`, so the
  server never deletes a destination. If the destination exists, an existing
  file is replaced with a second request with `Overwrite: T`; an existing
  collection is an error wrapping `os.ErrExist` for `Move` and an
  `fs.ErrIsDirectory` for `CopyFile`, and a collection moved onto a file is an
  `fs.ErrIsNotDirectory`. Moving a collection into itself is an error wrapping
  `os.ErrInvalid` without asking the server.
- **Case-insensitive servers.** An existing destination may be the source
  itself under another name: on a case-insensitive server if the paths only
  differ in case, or on a server that normalizes names if both are files with
  the same size and modification time. `Overwrite: T` would make the server
  delete it, so it is never sent for such a destination. `Move` moves the
  source to a temporary `.gofs-rename-*` name in its collection first and
  then onto the destination, which renames it on a case-insensitive server,
  and moves it back if that fails. `CopyFile` checks with a move to a
  temporary name and back whether the destination is the source, and then
  does nothing. An error that leaves the source at the temporary name
  contains that name.
- **Trailing slashes.** Servers differ in how they treat the trailing slash
  of collection URLs: nginx answers `DELETE` or `MKCOL` of a collection
  without it with `409 Conflict` and a file URL with it with `404`, Apache
  redirects a collection URL without it. The file system asks for the type of
  a path (`PROPFIND` with depth 0) before it deletes, moves or copies it, and
  sends collection URLs with and file URLs without the slash.
- **Redirects.** Go's HTTP client follows a `301`, `302` or `303` redirect of
  `PROPFIND`, `PUT`, `DELETE`, `MKCOL`, `MOVE` or `COPY` with a `GET`. The file
  system doesn't follow such redirects and redirects to the same URL with a
  trailing slash: a `PROPFIND` is repeated for the collection URL, a `GET` or
  `PUT` of a collection is an `fs.ErrIsDirectory`, every other one a
  `*webdavfs.StatusError`. Redirects that keep the method are followed, but
  not to another host or port than the one of the base URL, nor from `https`
  to `http`: Go would send `Options.Header` to any host and basic auth to the
  same host name, also over plain HTTP. A `CheckRedirect` of `Options.Client`
  decides about such a redirect instead; it gets the request without the
  `Authorization` header and the `Options.Header` entries and may add them
  back.
- **Permissions.** WebDAV has no permission model. The reported permissions
  are the constants `webdavfs.DefaultPermissions` /
  `webdavfs.DefaultDirPermissions`.
- **Errors.** 404 and 410 wrap `os.ErrNotExist`, 409 of `PUT`, `MKCOL`,
  `MOVE` and `COPY` wraps `os.ErrNotExist` for the missing parent
  collection, 405 on `MKCOL` wraps `os.ErrExist`, 401 and 403 wrap
  `os.ErrPermission`. Reading or writing a collection is an
  `fs.ErrIsDirectory`, `Remove` of a collection with members an
  `fs.ErrDirectoryNotEmpty`, `Remove` and `RemoveAll` of the root directory
  an error wrapping `os.ErrInvalid` without a request. A `207 Multi-Status`
  answer of `DELETE`, `MOVE` or `COPY` reports members that failed and every
  other error status becomes a `*webdavfs.StatusError` with the method, file
  and status.
  After `Close` every method returns `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against an in-process
`golang.org/x/net/webdav` handler served by `httptest`, once as it is and
once each with the trailing slash rules and redirects of nginx and Apache
added, so they need neither Docker nor a network.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
