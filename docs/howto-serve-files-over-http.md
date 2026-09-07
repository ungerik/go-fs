# How to serve files over HTTP

Serve a single file, a whole directory, or embedded assets through
`net/http`, with correct `Content-Type`, range requests and 404 handling.

## Prerequisites

- `github.com/ungerik/go-fs` in your `go.mod`
- A `net/http` server

## Serve a single file

```go
http.Handle("/report.pdf", fs.ServeFileHTTPHandler(fs.File("/srv/data/report.pdf")))
```

Or inside a handler you already have:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    fs.ServeFileHTTP(w, r, fs.File("/srv/data/report.pdf"))
}
```

Both use `http.ServeContent` underneath, so you get range requests,
`If-Modified-Since` and `ETag` handling for free. The `Content-Type` is deduced
from the file name, and from the content if the name is not enough. Pass it
explicitly to override:

```go
fs.ServeFileHTTP(w, r, file, "application/pdf")
```

Status codes are handled for you: `404` when the file does not exist, `500` for
any other read error. The 500 body is a generic status text, never the
underlying error string.

Both functions take a `fs.FileReader`, so an in-memory file works too:

```go
fs.ServeFileHTTP(w, r, fs.NewMemFile("report.csv", csvBytes))
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
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-assets", true) // read-only
if err != nil {
    return err
}
defer bucket.Close()

stdFS := bucket.RootDir().StdFS()
http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(stdFS))))
```

`StdFS` implements `io/fs.FS`, `io/fs.SubFS`, `io/fs.StatFS`,
`io/fs.ReadDirFS` and `io/fs.ReadFileFS`, so it also works with
`html/template.ParseFS`, `iofs.WalkDir` and anything else that takes a standard
library file system.

## Map errors to status codes

`ErrDoesNotExist` and `ErrPermission` implement `http.Handler`, so you can
serve them directly:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    data, err := fs.File("/srv/data").Join(r.PathValue("name")).ReadAll(r.Context())
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

Joining a user-supplied name is only safe because `Join` cleans the path and a
`SubFileSystem` cannot escape its directory. If the name comes from the client,
serve from a `SubFileSystem` or `StdFS` rather than joining onto a local path:

```go
subFS, err := fs.NewSubFileSystemAndRegister(fs.Local, "/srv/data", "data")
if err != nil {
    return err
}
defer subFS.Close()
// paths under sub://data/ can't escape /srv/data
```

## Serve embedded assets

```go
//go:embed templates/*
var templates embed.FS

tmplFS := fs.NewStdFileSystemAndRegister(templates, "templates")
defer tmplFS.Close()

html, err := fs.File("stdfs://templates/templates/index.html").ReadAllString(ctx)
```

`StdFileSystem` is the counterpart of `StdFS`: it adapts any `io/fs.FS` into a
go-fs file system, so `embed.FS`, `os.DirFS`, a `zip.Reader` or a
`testing/fstest.MapFS` all work with the `File` API.

To serve embedded defaults that the running program can override, stack a
writable layer on top:

```go
defaults := fs.NewStdFileSystemAndRegister(embeddedConfig, "defaults")
scratch, err := fs.NewMemFileSystem("/")
if err != nil {
    return err
}
overlay, err := fs.NewOverlayFileSystemAndRegister(defaults, scratch, "config")
if err != nil {
    return err
}
defer overlay.Close()

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
through `http.ServeContent` with a working seeker.

## Troubleshooting

**Range requests download the whole file first** — `ServeFileHTTP` calls
`OpenReadSeeker`, and on a backend without native seeking that buffers the
whole file. Check the "Seeking reads" row of the support matrix in
[FileSystem interfaces](reference-filesystem-interfaces.md); `azureblobfs`,
`sftpfs`, `smbfs` and `webdavfs` seek natively.

**`http.FS` will not accept my file system** — it takes an `io/fs.FS`. Call
`.StdFS()` on a `File` first; do not pass the `fs.FileSystem` itself.

**404 for a file that exists** — `http.FileServer` paths are relative to the
`StdFS` root. Check your `http.StripPrefix` prefix matches the mount path.

**Wrong `Content-Type`** — detection uses the file extension first. Pass the
type explicitly as the last argument to `ServeFileHTTP`.

## Related

- [Errors](reference-errors.md) — the error types that serve themselves
- [Work with archives](howto-work-with-archives.md)
- [Handle form uploads](howto-handle-form-uploads.md)
- [Getting started](tutorial-getting-started.md)
