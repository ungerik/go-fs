# How to serve files over HTTP

Serve a single file, a whole directory, or embedded assets through
`net/http`, with correct `Content-Type`, range requests and 404 handling.

## Prerequisites

- `github.com/ungerik/go-fs` in your `go.mod`
- A `net/http` server

## Serve a single file

```go
http.Handle("/report.pdf", fs.ServeFileHTTPHandler(fs.File("/srv/data/report.pdf"), ""))
```

Or inside a handler you already have:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    fs.ServeFileHTTP(w, r, fs.File("/srv/data/report.pdf"), "")
}
```

Both use `http.ServeContent` underneath, so you get range requests,
`If-Modified-Since` and `HEAD` handling for free. The last argument is the
`Content-Type`; with `""` a `Content-Type` header that is already set is
used, or else the type is deduced from the file name, and from the content if
the name is not enough. Pass it explicitly to override:

```go
fs.ServeFileHTTP(w, r, file, "application/pdf")
```

Status codes are handled for you: `404` when the file does not exist or is a
directory, `403` when reading it is not permitted, `500` for any other error.
The error bodies are generic status texts, never the underlying error string.

The content is streamed from `OpenReader`, never read into memory. A reader
that can seek, like the `*os.File` of a local file, goes to `http.ServeContent`
directly, so `net/http` sends a local file with `sendfile`. For a reader that
can't seek the size and modification time come from `Stat`, so a `HEAD`
request doesn't read the content unless it is needed to deduce the type.
Such a reader serves one range at most: a request for several ranges gets the
full content with status `200`, because each range before the previous one
would read the file again from the start. If `Stat` reports no size, like for
an HTTP URL without `Content-Length`, the content is streamed with status `200`
and without `Content-Length`, ranges and conditional requests.

Both functions take a `fs.FileReader`, so an in-memory file works too:

```go
fs.ServeFileHTTP(w, r, fs.NewMemFile("report.csv", csvBytes), "text/csv")
```

Modification time is only sent for a real `fs.File`; a `MemFile` has none, so
conditional requests fall back to unconditional.

### Always use the request context

```go
func handler(w http.ResponseWriter, r *http.Request) {
    data, err := fs.File("/srv/data/report.json").ReadAll(r.Context())
    ...
}
```

`r.Context()` is cancelled when the client disconnects, so a large read stops
instead of finishing into a dead socket.

## Serve a directory

`File.StdFS()` exposes a directory as an `io/fs.FS`, which `http.FS` accepts:

```go
stdFS := fs.File("/srv/static").StdFS()

http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(stdFS))))
```

This works for **any** backend, not just local. The same three lines serve an
S3 bucket, an SFTP directory or a ZIP archive:

```go
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-assets", &s3fs.Options{ReadOnly: true})
if err != nil {
    return err
}
defer bucket.Close()
err = fs.Register(bucket)
if err != nil {
    return err
}

stdFS := bucket.RootDir().StdFS()
http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(stdFS))))
```

`StdFS` implements `io/fs.FS`, `io/fs.SubFS`, `io/fs.StatFS`,
`io/fs.ReadDirFS` and `io/fs.ReadFileFS`, so it also works with
`html/template.ParseFS`, `iofs.WalkDir` and anything else that takes a standard
library file system.

## Map errors to status codes

`ErrDoesNotExist` and `ErrPermission` implement `http.Handler`, so you can
serve them directly. This handler reads the file named in the request from a
`SubFileSystem` registered as `sub://data`, see below:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    data, err := fs.File("sub://data/").Join(r.PathValue("name")).ReadAll(r.Context())
    if err != nil {
        var notExist fs.ErrDoesNotExist
        if errors.As(err, &notExist) {
            notExist.ServeHTTP(w, r) // 404
            return
        }
        var noPerm fs.ErrPermission
        if errors.As(err, &noPerm) {
            noPerm.ServeHTTP(w, r) // 403
            return
        }
        log.Printf("read %s: %v", r.PathValue("name"), err)
        http.Error(w, "could not read file", http.StatusInternalServerError)
        return
    }
    w.Write(data)
}
```

Log the real error, send an abstract description. Never put the underlying
error string in a 500 response — it leaks paths and internal structure.

Don't join a name that comes from the client onto a local path: `Join` cleans
the path, so `../../etc/passwd` below `/srv/data` is `/etc/passwd`. A
`SubFileSystem` has the directory as its root, and `..` can't leave it, so
serve such names from one, or from a `StdFS`. Over `fs.Local` both also
reject the reserved device names of Windows, like `NUL` or `COM1`, and names
with a colon, which Windows would open as a device or an alternate data stream:

```go
err := fs.NewSubFileSystemAndRegister(fs.Local, "/srv/data", "data")
if err != nil {
    return err
}
// paths under sub://data/ can't escape /srv/data
```

## Serve embedded assets

```go
//go:embed templates/*
var templates embed.FS

err := fs.NewStdFileSystemAndRegister(templates, "templates")
if err != nil {
    return err
}

html, err := fs.File("stdfs://templates/templates/index.html").ReadAllString(ctx)
```

`StdFileSystem` is the counterpart of `StdFS`: it adapts any `io/fs.FS` into a
go-fs file system, so `embed.FS`, `os.DirFS`, a `zip.Reader` or a
`testing/fstest.MapFS` all work with the `File` API.

To serve embedded defaults that the running program can override, stack a
writable layer on top. Both layers must stay registered while the overlay is
used:

```go
defaults := fs.NewStdFileSystem(embeddedConfig, "defaults")
err := fs.Register(defaults)
if err != nil {
    return err
}
scratch, err := fs.NewMemFileSystem(nil)
if err != nil {
    return err
}
err = fs.Register(scratch)
if err != nil {
    return err
}
err = fs.NewOverlayFileSystemAndRegister(defaults, scratch, "config")
if err != nil {
    return err
}

// reads fall through to the embedded defaults, writes land in memory
err = fs.File("overlay://config/app.json").WriteAllString(ctx, "...")
```

## Zip files on the fly

```go
func downloadHandler(w http.ResponseWriter, r *http.Request) {
    files, err := fs.File("/srv/reports").ListDirMax(r.Context(), -1, "*.pdf")
    if err != nil {
        http.Error(w, "could not list reports", http.StatusInternalServerError)
        return
    }
    data, err := fs.Zip(r.Context(), fs.AsFileReaders(files)...)
    if err != nil {
        http.Error(w, "could not build archive", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/zip")
    w.Header().Set("Content-Disposition", `attachment; filename="reports.zip"`)
    w.Write(data)
}
```

`fs.Zip` builds the archive in memory with `flate.BestCompression`, so it suits
a handful of reports, not a hundred gigabytes.

## Verification

```bash
curl -I http://localhost:8080/report.pdf          # 200, Content-Type, Accept-Ranges
curl -I http://localhost:8080/missing.pdf         # 404
curl -r 0-99 http://localhost:8080/report.pdf -o - | wc -c   # 100
```

The range request is the interesting one: it confirms the file was served
through `http.ServeContent`.

## Troubleshooting

**Range requests far into a large file are slow** — on a backend whose readers
can't seek, `ServeFileHTTP` reaches the requested offset by reading and
discarding the bytes before it, and it answers a request for several ranges
with the full content. Check the "Seeking reads" row of the support
matrix in [FileSystem interfaces](reference-filesystem-interfaces.md);
`azureblobfs`, `sftpfs`, `smbfs` and `webdavfs` seek natively, like the local
and the memory file systems.

**`http.FS` will not accept my file system** — it takes an `io/fs.FS`. Call
`.StdFS()` on a `File` first; do not pass the `fs.FileSystem` itself.

**404 for a file that exists** — `http.FileServer` paths are relative to the
`StdFS` root. Check your `http.StripPrefix` prefix matches the mount path.

**404 for a directory** — `ServeFileHTTP` serves files only; serve a directory
with `http.FileServer` and `StdFS` as above.

**Wrong `Content-Type`** — detection uses the file extension first. Pass the
type explicitly as the last argument to `ServeFileHTTP`.

## Related

- [Errors](reference-errors.md) — the error types that serve themselves
- [Work with archives](howto-work-with-archives.md)
- [Handle form uploads](howto-handle-form-uploads.md)
- [Getting started](tutorial-getting-started.md)
