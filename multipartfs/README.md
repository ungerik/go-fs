# Multipart Form File System

A read-only [go-fs](https://github.com/ungerik/go-fs) file system for the
files of a parsed multipart HTTP form, so an uploaded file can be passed to
any code that takes an `fs.File` or `fs.FileReader` — without first copying
it somewhere.

This package is part of the `github.com/ungerik/go-fs` module, no separate
`go get` is needed.

## Usage

```go
import (
    "net/http"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/multipartfs"
)

func handleUpload(response http.ResponseWriter, request *http.Request) {
    ctx := request.Context()

    // maxMemory 4 MB, "" for a random id
    formFS, err := multipartfs.NewFromRequest(request, 4*1024*1024, "")
    if err != nil {
        http.Error(response, "invalid form", http.StatusBadRequest)
        return
    }
    // Register the file system so that the fs.File values of the uploads work
    if err := fs.Register(formFS); err != nil {
        http.Error(response, err.Error(), http.StatusInternalServerError)
        return
    }
    // Close unregisters the file system and removes the temporary files of the form
    defer formFS.Close()

    // Regular form values
    comment := formFS.FormValue("comment")

    // Uploaded files as fs.File
    file, err := formFS.FormFile("attachment")
    files := formFS.FormFiles("attachments") // all files of one field

    data, err := file.ReadAll(ctx)
    name := file.Name()
    size := file.Size()
}
```

`multipartfs.New(form, id)` does the same for a `*multipart.Form` that was
parsed by the caller. Neither registers the file system, so register it
before using the returned `fs.File` values. `formFS.Form` is the underlying
`*multipart.Form`, and
`GetMultipartFileHeader` returns the `*multipart.FileHeader` of a path, for
the `Content-Type` a client sent for example.

The root package also has `fs.ParseRequestMultipartFormMemFiles` and
`fs.ReadMultipartFormMemFiles`, which read the uploaded files into
`fs.MemFile` values instead of exposing them as a file system.

## How multipart forms map to the file system

- **Two levels.** The form fields that have uploaded files are the
  directories of the file system, the uploaded files are their entries, so
  a path has the form `/<form field>/<file name>`.
- **Unique names.** A path has to identify exactly one file, so files
  uploaded under an already used name get a unique name (`a.txt`,
  `a (2).txt`, ...), and names that are not usable as a path element (`.`,
  `..`) become `unnamed`. All other names are kept as uploaded, including
  names that look percent-encoded like `report%201.pdf`.
- **Escaped field names.** Form field names can contain anything, so the
  directory name of a field escapes `%` as `%25` and `/` as `%2F`, and the
  fields `.` and `..` become `%2E` and `%2E%2E`. Ordinary field names like
  `attachments` are used as they are. Paths are split before the field
  name is unescaped, so no field or file name can address an upload of
  another field.
- **Read-only.** `Exists` and `ReadAll` are native, every write operation
  returns `fs.ErrReadOnlyFileSystem`. Uploaded files have the permissions
  `fs.AllRead`, also in the `Stat` of an open reader.
- **No modification time.** A multipart form carries no timestamps, so
  uploaded files have a zero modification time.
- **Lifetime.** `Close` unregisters the file system, closes the readers of
  uploaded files that are still open and removes the temporary files that
  `http.Request.ParseMultipartForm` wrote to disk. Afterwards every method
  that returns an error returns `fs.ErrFileSystemClosed`; `FormValue`,
  `FormValues` and the values of `formFS.Form` stay usable, because they are
  kept in memory. If the temporary files can't be removed, `Close` returns
  the error and the next `Close` tries again. Always `defer formFS.Close()`
  in the handler.
- **URI prefix.** `multipart://` followed by the id passed to `New` or
  `NewFromRequest`, a random one if it is empty, so concurrent requests each
  get their own file system. `ID()` is that prefix.

## Testing

The tests run the go-fs conformance suite against a form built with
`net/http/httptest`, so they need no network access.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
