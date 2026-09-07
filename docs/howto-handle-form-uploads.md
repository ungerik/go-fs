# How to handle multipart form uploads

Treat the files uploaded in an HTML form as regular `fs.File` values, so the
code that processes an upload is the same code that processes a file from disk
or S3.

## Prerequisites

- `github.com/ungerik/go-fs` in your `go.mod`
- An HTTP handler receiving a `multipart/form-data` request
- `multipartfs` is a package of the **root module**, so no extra `go get`

## Steps

### 1. Parse the request form into a file system

```go
import "github.com/ungerik/go-fs/multipartfs"

const maxUploadSize = 32 << 20 // 32 MB kept in memory, the rest spills to temp files

func uploadHandler(w http.ResponseWriter, r *http.Request) {
    formFS, err := multipartfs.FromRequestForm(r, maxUploadSize)
    if err != nil {
        http.Error(w, "could not parse upload", http.StatusBadRequest)
        return
    }
    defer formFS.Close()
    ...
}
```

`Close` removes the temporary files the form spilled to disk, so the `defer` is
not optional. After it, every method returns `fs.ErrFileSystemClosed`.

The `maxMemory` argument is what `http.Request.ParseMultipartForm` means by it:
how much is buffered in memory before spilling to temp files, not a hard limit
on the upload. Cap the request itself with `http.MaxBytesReader` if you need
one.

### 2. Read the non-file form values

```go
email := formFS.FormValue("email")       // first value
tags := formFS.FormValues("tags")        // all values
```

`formFS.Form` is the underlying `*multipart.Form` if you need it directly.

### 3. Get the uploaded files

```go
file, err := formFS.FormFile("attachment")   // first file of that field
if err != nil {
    http.Error(w, "missing attachment", http.StatusBadRequest)
    return
}

files := formFS.FormFiles("attachments")     // all files of that field
```

`FormFile` returns `fs.ErrDoesNotExist` when the field has no files, so
`errors.Is(err, os.ErrNotExist)` distinguishes "no upload" from a real failure.
`FormFiles` returns nil rather than an error.

From here everything is the normal API:

```go
name := file.Name()
size := file.Size()
data, err := file.ReadAll(r.Context())
```

To reach the part's MIME header — its declared `Content-Type`, for example:

```go
header, err := formFS.GetMultipartFileHeader(file.Path())
if err != nil {
    return err
}
contentType := header.Header.Get("Content-Type")
```

Treat that value as a client-supplied hint, not as fact.

### 4. Hand the file to code that takes a FileReader

This is the point of the package. A function written against `fs.FileReader`
accepts an upload with no adapter:

```go
func Ingest(ctx context.Context, f fs.FileReader) error { ... }

err = Ingest(r.Context(), file)
```

### 5. Persist the upload

Copy it anywhere, on any backend:

```go
err = fs.CopyFile(r.Context(), file, fs.File("/srv/uploads/"))
err = fs.CopyFile(r.Context(), file, fs.File("s3://bucket/uploads/"))
```

A destination that is an existing directory gets a file with the source's base
name. Missing parent directories are created.

To buffer it in memory instead:

```go
memFile, err := fs.ReadMemFile(r.Context(), file)
```

## Working with an already-parsed form

If you parsed the form yourself, wrap it instead:

```go
err := r.ParseMultipartForm(maxUploadSize)
if err != nil {
    return err
}
formFS := multipartfs.New(r.MultipartForm)
defer formFS.Close()
```

## Without a file system: ParseRequestMultipartFormMemFiles

When you just want the bytes and no file system, the root package has a
shortcut that reads everything into memory:

```go
filesByField, err := fs.ParseRequestMultipartFormMemFiles(r, maxUploadSize)
if err != nil {
    return err
}
for field, memFiles := range filesByField {
    for _, mf := range memFiles {
        fmt.Println(field, mf.FileName, len(mf.FileData))
    }
}
```

`fs.ReadMultipartFormMemFiles(ctx, form)` does the same for a form you already
parsed. Use these for small uploads; use `multipartfs` when the upload may be
large or when you want to stream it somewhere without buffering.

## How the file system is laid out

It has exactly two levels: **the form field names are the directories**, and
the files uploaded under a field are the files in that directory.

```
multipart://<id>/attachment/report.pdf
                 ^field      ^uploaded name
```

Three normalisations happen because a path has to identify exactly one file:

- **Duplicate names get a suffix.** A second file named `a.txt` in the same
  field becomes `a (2).txt`, the third `a (3).txt`, before the extension.
- **Unusable names are replaced.** A name that is not a valid path element,
  such as `.` or `..`, becomes `unnamed`.
- **There are no modification times.** Uploaded files carry none.

The file system is read-only. It also provides `Exists` and `ReadAll` natively.

## Verification

```bash
curl -F "email=alice@example.com" -F "attachment=@report.pdf" \
     http://localhost:8080/upload
```

```go
err = formFS.RootDir().ListDirRecursive(r.Context(), func(f fs.File) error {
    log.Printf("%s (%d bytes)", f.Path(), f.Size())
    return nil
})
```

## Troubleshooting

**`file system is closed`** — the handler returned and the `defer
formFS.Close()` ran. An upload passed to a background goroutine must be copied
first, with `fs.ReadMemFile` or `fs.CopyFile`, because the temp files are gone
once the handler returns.

**Temp files left behind** — `Close` was not called. Every path out of the
handler needs it; a `defer` right after the successful parse covers them all.

**`FormFile` returns "does not exist"** — the field name does not match the
form's `name` attribute, or the field carries a value rather than a file. Check
`formFS.Form.File` for the field names actually present.

**A file arrives named `unnamed`** — the client sent a filename that is not
usable as a path element. Use the field name and your own naming scheme if the
original matters.

**Two uploads collide** — they did not; the second was renamed to `a (2).txt`.
Iterate with `FormFiles` rather than calling `FormFile` twice.

**Large uploads use too much disk** — `maxMemory` only controls the
memory/temp-file split. Limit the request body with `http.MaxBytesReader`.

## Related

- [Serve files over HTTP](howto-serve-files-over-http.md)
- [Copy, move and compare](howto-copy-move-and-compare.md)
- [Work with archives](howto-work-with-archives.md) — zip the uploads back up
- [Getting started](tutorial-getting-started.md)
