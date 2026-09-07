# Errors

Every error go-fs returns is checkable with `errors.Is` against a standard
library sentinel, so code that already handles `os.ErrNotExist` keeps working
no matter which backend produced the error.

go-fs uses only the standard library `errors` and `fmt` packages. Everything
in this document lives in `errors.go`.

## Check errors by sentinel, not by type

This is the whole point of the design. Prefer the left column:

```go
data, err := file.ReadAll(ctx)
switch {
case errors.Is(err, os.ErrNotExist):
    // missing, on any backend
case errors.Is(err, fs.ErrReadOnlyFileSystem):
    // wrong direction
case errors.Is(err, errors.ErrUnsupported):
    // this backend can't do it
case err != nil:
    return err
}
```

Use the concrete types only when you need the `File` the error is about:

```go
var notExist fs.ErrDoesNotExist
if errors.As(err, &notExist) {
    if f, ok := notExist.File(); ok {
        log.Printf("missing: %s", f.URL())
    }
}
```

## Sentinel errors

`SentinelError` is a string type, so these are untyped constants and can be
compared directly or with `errors.Is`.

| Constant                 | Message                     | Returned when                                      |
| ------------------------ | --------------------------- | -------------------------------------------------- |
| `ErrReadOnlyFileSystem`  | `file system is read-only`  | A write is attempted on a file system whose `ReadableWritable` reports `writable == false`, or that does not implement `WriteFileSystem`. |
| `ErrWriteOnlyFileSystem` | `file system is write-only` | A read is attempted on a file system whose `ReadableWritable` reports `readable == false`, such as `zipfs.Writer`. |
| `ErrInvalidFileSystem`   | `invalid file system`       | The `InvalidFileSystem` is used.                   |
| `ErrFileSystemClosed`    | `file system is closed`     | Any method is called after `Close`.                |
| `ErrUnmarshalJSON`       | `can't unmarshal JSON`      | `ReadJSON` fails to unmarshal.                     |
| `ErrMarshalJSON`         | `can't marshal JSON`        | `WriteJSON` fails to marshal.                      |
| `ErrUnmarshalXML`        | `can't unmarshal XML`       | `ReadXML` fails to unmarshal.                      |
| `ErrMarshalXML`          | `can't marshal XML`         | `WriteXML` fails to marshal.                       |

The read-only and write-only errors are wrapped with the file system name, so
the message reads `LocalFileSystem: file system is read-only`.

## Typed errors

| Type                | Wraps (`errors.Is`)     | Carries                                 | Extra                                              |
| ------------------- | ----------------------- | --------------------------------------- | -------------------------------------------------- |
| `ErrDoesNotExist`   | `os.ErrNotExist`        | `File` or `FileReader`                  | Implements `http.Handler`, responding with `http.NotFound` |
| `ErrPermission`     | `os.ErrPermission`      | `File`                                  | Implements `http.Handler`, responding with `403 Forbidden` |
| `ErrAlreadyExists`  | `os.ErrExist`           | `File`                                  |                                                    |
| `ErrIsDirectory`    | —                       | `File` or `FileReader`                  |                                                    |
| `ErrIsNotDirectory` | —                       | `File` or `FileReader`                  |                                                    |
| `ErrUnsupported`    | `errors.ErrUnsupported` | The `FileSystem` and the operation name |                                                    |

`ErrIsDirectory` and `ErrIsNotDirectory` wrap nothing, so check them with
`errors.As` or a direct `errors.Is` against a value of the same type.

### Constructors

```go
fs.NewErrDoesNotExist(file)              // from a File
fs.NewErrDoesNotExistFileReader(reader)  // from a FileReader
fs.NewErrPathDoesNotExist(path)          // from a plain string path
fs.NewErrPermission(file)
fs.NewErrAlreadyExists(file)
fs.NewErrIsDirectory(fileOrReader)       // takes any
fs.NewErrIsNotDirectory(fileOrReader)    // takes any
fs.NewErrUnsupported(fileSystem, "Touch of an existing file")
```

### Reading the File back out

`ErrDoesNotExist`, `ErrIsDirectory` and `ErrIsNotDirectory` can be about a
`File` or about a `FileReader` (a `MemFile`, for example), so their accessors
return an `ok` flag:

```go
func (err ErrDoesNotExist) File() (file File, ok bool)
func (err ErrDoesNotExist) FileReader() (file FileReader, ok bool)
```

`ErrPermission.File()` and `ErrAlreadyExists.File()` are always about a `File`
and return it directly.

## ErrEmptyPath

```go
var ErrEmptyPath = NewErrDoesNotExist(InvalidFile)
```

An empty path is a missing file, so `ErrEmptyPath` satisfies
`errors.Is(err, os.ErrNotExist)` like any other `ErrDoesNotExist`. Its message
is `empty file path`. Every dispatch entry point rejects an empty path with it
before touching the backend.

## RemoveErrDoesNotExist

Deleting something that is already gone is usually success, not failure. This
helper collapses that case:

```go
// nil if err is nil or wraps os.ErrNotExist, else err unchanged
func RemoveErrDoesNotExist(err error) error
```

`fs.Remove`, `fs.RemoveFiles` and the recursive removal fallbacks use it, which
is why they skip missing files instead of reporting them.

```go
err := fs.RemoveErrDoesNotExist(file.Remove())
```

## Serving errors over HTTP

`ErrDoesNotExist` and `ErrPermission` implement `http.Handler`, so an error can
be served directly:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    data, err := fs.File("/srv/data/report.pdf").ReadAll(r.Context())
    if err != nil {
        var notExist fs.ErrDoesNotExist
        if errors.As(err, &notExist) {
            notExist.ServeHTTP(w, r) // 404
            return
        }
        http.Error(w, "read failed", http.StatusInternalServerError)
        return
    }
    w.Write(data)
}
```

Never pass the raw error string to the client on a 500; use a short abstract
description as above.

## What each condition maps to

Summary of the contract every backend follows:

| Condition                        | Check                                       |
| -------------------------------- | ------------------------------------------- |
| File does not exist              | `errors.Is(err, os.ErrNotExist)`            |
| `MakeDir` on an existing path    | `errors.Is(err, os.ErrExist)`               |
| Write on a read-only file system | `errors.Is(err, fs.ErrReadOnlyFileSystem)`  |
| Read on a write-only file system | `errors.Is(err, fs.ErrWriteOnlyFileSystem)` |
| Use after `Close`                | `errors.Is(err, fs.ErrFileSystemClosed)`    |
| Backend can't do the operation   | `errors.Is(err, errors.ErrUnsupported)`     |
| Context cancelled                | `errors.Is(err, context.Canceled)`          |

## Related

- [FileSystem interfaces](reference-filesystem-interfaces.md) — which operations can return `ErrUnsupported`
- [Serve files over HTTP](howto-serve-files-over-http.md)
- [Run the conformance suite](howto-run-the-conformance-suite.md) — the suite checks this contract
