# WebDAV File System

A [go-fs](https://github.com/ungerik/go-fs) file system for WebDAV servers:
Nextcloud, ownCloud, Apache `mod_dav`, nginx, SharePoint or the
`golang.org/x/net/webdav` handler.

The client is implemented with the standard library only, so one module
covers every server.

## Installation

```bash
go get github.com/ungerik/go-fs/webdavfs
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

    davFS, err := webdavfs.NewAndRegister(
        ctx,
        "https://cloud.example.com/remote.php/dav/files/alice",
        &webdavfs.Options{Username: "alice", Password: "app-password"},
    )
    if err != nil {
        panic(err)
    }
    defer davFS.Close()

    // Use it like any other go-fs file system
    file := davFS.RootDir().Join("Documents", "notes.txt")

    err = file.WriteAllString(ctx, "Hello, WebDAV!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

`New` returns a file system without registering it, `NewAndRegister`
registers it so `fs.File` values with its prefix work.

File URIs have the form `webdav://<host><base path>/<path>`, so the example
above is reachable as
`webdav://cloud.example.com/remote.php/dav/files/alice/Documents/notes.txt`.
`davFS.ID()` is that prefix without the scheme.

### Authentication

`Options.Username` and `Options.Password` are sent as HTTP basic auth and
override credentials embedded in the base URL. `Options.Header` is added to
every request, for bearer tokens for example, and `Options.Client` replaces
the `*http.Client` used for all requests:

```go
&webdavfs.Options{
    Header: http.Header{"Authorization": {"Bearer " + token}},
    Client: &http.Client{Timeout: 30 * time.Second},
}
```

## How WebDAV concepts map to the file system

- **Paths.** File system paths map directly to URL paths below the base URL.
- **Methods.** `PROPFIND` backs `Stat` and `ListDir`, `GET` backs reads,
  `PUT` backs writes, `MKCOL` backs `MakeDir`, `DELETE` backs `Remove`, and
  `MOVE` and `COPY` back `Move` and `CopyFile`.
- **Native operations.** `ReadAll`, `WriteAll`, `CopyFile`, `Move` and
  `RemoveAll` are native, and readers seek with `Range` requests; everything
  else uses the generic emulation of the `fs` package.
- **Permissions.** WebDAV has no permission model. The reported permissions
  are `webdavfs.DefaultPermissions` / `webdavfs.DefaultDirPermissions`.
- **Errors.** 404 wraps `os.ErrNotExist`, 405 on `MKCOL` wraps
  `os.ErrExist`, 401 and 403 wrap `os.ErrPermission`, and every other error
  status becomes a `*webdavfs.StatusError` with the method, file and status.
  After `Close` every method returns `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against an in-process
`golang.org/x/net/webdav` handler served by `httptest`, so they need
neither Docker nor a network.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
