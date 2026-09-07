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

    formFS, err := multipartfs.FromRequestForm(request, 4*1024*1024) // maxMemory
    if err != nil {
        http.Error(response, "invalid form", http.StatusBadRequest)
        return
    }
    // Close removes the temporary files of the form
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

`multipartfs.New` does the same for a `*multipart.Form` that was parsed by
the caller. `formFS.Form` is the underlying `*multipart.Form`, and
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
  `..`) become `unnamed`.
- **Read-only.** `Exists` and `ReadAll` are native, every write operation
  returns `fs.ErrReadOnlyFileSystem`.
- **No modification time.** A multipart form carries no timestamps, so
  uploaded files have a zero modification time.
- **Lifetime.** `Close` removes the temporary files that
  `http.Request.ParseMultipartForm` wrote to disk and unregisters the file
  system; every method returns `fs.ErrFileSystemClosed` afterwards. Always
  `defer formFS.Close()` in the handler.
- **URI prefix.** `multipart://` followed by a random id, so concurrent
  requests each get their own file system. `ID()` is that whole prefix.

## Testing

The tests run the go-fs conformance suite against a form built with
`net/http/httptest`, so they need no network access.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
