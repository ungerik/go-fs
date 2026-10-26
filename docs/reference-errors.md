# Errors

Every error go-fs returns is checkable with `errors.Is`, either against a
standard library sentinel, so code that already handles `os.ErrNotExist` keeps
working no matter which backend produced the error, or against a go-fs error
type.

go-fs uses only the standard library `errors` and `fmt` packages. Everything
in this document lives in `errors.go`.

## Check errors with errors.Is

This is the whole point of the design:

```go
data, err := file.ReadAll(ctx)
switch {
case errors.Is(err, os.ErrNotExist):
    // missing, on any backend
case errors.Is(err, fs.ErrIsDirectory{}):
    // the file is a directory
case errors.Is(err, fs.ErrReadOnlyFileSystem):
    // wrong direction
case errors.Is(err, errors.ErrUnsupported):
    // this backend can't do it
case err != nil:
    return err
}
_ = data
```

Every typed error has an `Is` method that matches any value of its own type,
so `errors.Is(err, fs.ErrIsDirectory{})` is true for an `ErrIsDirectory` about
any file. Two separately created errors about a file are never `==`, so always
compare with `errors.Is` or `errors.As`.

Use `errors.As` only when you need the `File` the error is about:

```go
var notExist fs.ErrDoesNotExist
if errors.As(err, &notExist) {
    if f, ok := notExist.File(); ok {
        log.Printf("missing: %s", f.URI())
    }
}
```

## Sentinel errors

`SentinelError` is a string type, so these are typed constants that can be
compared directly or with `errors.Is`.

| Constant                   | Message                                 | Returned when                                      |
| -------------------------- | --------------------------------------- | -------------------------------------------------- |
| `ErrReadOnlyFileSystem`    | `file system is read-only`              | A write is attempted on a file system whose `ReadableWritable` reports `writable == false`, or that does not implement `WriteFileSystem`. |
| `ErrWriteOnlyFileSystem`   | `file system is write-only`             | A read is attempted on a file system whose `ReadableWritable` reports `readable == false`, such as `zipfs.Writer`. |
| `ErrInvalidFileSystem`     | `invalid file system`                   | Every operation on a file of the invalid file system, see below. |
| `ErrFileSystemClosed`      | `file system is closed`                 | Any method is called after `Close`.                |
| `ErrEmptyPath`             | `empty file path`                       | An operation is called with an empty path.         |
| `ErrFileSystemsDoNotMatch` | `file systems do not match`             | `File.RelPathOf` or `File.CreateSymbolicLink` with files of two different file systems. |
| `ErrAlreadyRegistered`     | `file system prefix already registered` | `Register` finds another file system registered for the prefix or one of the prefix aliases. |
| `ErrUnmarshalJSON`         | `can't unmarshal JSON`                  | `ReadJSON` fails to unmarshal.                     |
| `ErrMarshalJSON`           | `can't marshal JSON`                    | `WriteJSON` or `WriteJSONIndent` fails to marshal. |
| `ErrUnmarshalXML`          | `can't unmarshal XML`                   | `ReadXML` fails to unmarshal.                      |
| `ErrMarshalXML`            | `can't marshal XML`                     | `WriteXML` or `WriteXMLIndent` fails to marshal.   |

The read-only and write-only errors are wrapped with the `Name()` of the file
system, so the message reads `memory file system: file system is read-only`.

### ErrInvalidFileSystem

A URI with a scheme that no file system is registered for, like
`s3://bucket/x` in a program that didn't register `s3fs`, resolves to the
invalid file system. Every operation on it returns an error wrapping
`ErrInvalidFileSystem` that names the URI, with the password of the URI
replaced by `xxxxx`:

```
invalid file system: no file system registered for "s3://bucket/x"
invalid file system: no file system registered for "sftp://user:xxxxx@host/x"
```

The error does not wrap `os.ErrNotExist`, because the file is not missing, it
can't be accessed at all. `Exists` and `IsDir` are false.

### ErrEmptyPath

An empty path is a programming error, not a missing file, so `ErrEmptyPath`
does **not** wrap `os.ErrNotExist`. `fs.File("").Remove()` returns it, and
`RemoveErrDoesNotExist` doesn't swallow it. Every dispatch entry point
rejects an empty path with it before touching the backend.

## Typed errors

| Type                   | Wraps (`errors.Is`)     | Carries                                 | Extra                                              |
| ---------------------- | ----------------------- | --------------------------------------- | -------------------------------------------------- |
| `ErrDoesNotExist`      | `os.ErrNotExist`        | `File` or other `FileReader`            | Implements `http.Handler`, responding with `http.NotFound` |
| `ErrPermission`        | `os.ErrPermission`      | `File` or other `FileReader`            | Implements `http.Handler`, responding with `403 Forbidden` |
| `ErrAlreadyExists`     | `os.ErrExist`           | `File` or other `FileReader`            | Moving onto an existing directory, writing an archive entry twice |
| `ErrIsDirectory`       | —                       | `File` or other `FileReader`            | Reading or writing a directory, copying onto one   |
| `ErrIsNotDirectory`    | —                       | `File` or other `FileReader`            | A path below a file, moving a directory onto a file, `File.CheckIsDir` of a file |
| `ErrDirectoryNotEmpty` | —                       | `File` or other `FileReader`            | `Remove` of a non-empty directory                  |
| `ErrUnsupported`       | `errors.ErrUnsupported` | The `String()` of the `FileSystem` and the operation name |                          |

`ErrIsDirectory`, `ErrIsNotDirectory` and `ErrDirectoryNotEmpty` wrap
nothing; check them with `errors.Is(err, fs.ErrIsDirectory{})`,
`errors.Is(err, fs.ErrIsNotDirectory{})` and
`errors.Is(err, fs.ErrDirectoryNotEmpty{})`.

The messages name the file: the URI of a `File` with its password redacted
(and without the query for `URIFileSystem` files, which can carry signatures),
the `FileName` of a `MemFile`, the `Name` of other readers:

```
file does not exist: mem://fixtures/missing.txt
file is a directory: file:///srv/data
directory is not empty: sftp://user@host/home/user/dir
```

The password is also redacted from a URI that is not a valid URL,
like one with a file named `100%.txt` or a password containing `/`,
from a URI like `ftp://user:123/x@host/file` that `url.Parse` reads
as the host `user` with the port `123`, and from a URI like
`ftp://user:S3cr@et/Pw@host/file` that `url.Parse` reads as the host `et`.
An `@` in the path of a URI with a password can't be told apart from one
of the password, so `ftp://user:secret@host/me@example.com` is named as
`ftp://user:xxxxx@example.com`, see `fsimpl.RedactURI`.

### Constructors

```go
fs.NewErrDoesNotExist(file)        // for a File or another FileReader
fs.NewErrPathDoesNotExist(path)    // for a name or path that is not a File
fs.NewErrPermission(file)
fs.NewErrAlreadyExists(file)
fs.NewErrIsDirectory(file)
fs.NewErrIsNotDirectory(file)
fs.NewErrDirectoryNotEmpty(file)
fs.NewErrUnsupported(fileSystem, "Touch of an existing file")
```

The file constructors take a `FileReader`, so a `File` or a `MemFile`.
`NewErrPathDoesNotExist` stores the path as the `FileName` of a `MemFile`, so
it can't be mistaken for a local file.

### Reading the File back out

The typed file errors can be about a `File` or about another `FileReader` (a
`MemFile`, for example), so every one of them has two accessors:

```go
func (err ErrDoesNotExist) File() (file File, ok bool)
func (err ErrDoesNotExist) FileReader() FileReader
```

`File` returns false if the error is about a `FileReader` that is not a
`File`. `FileReader` returns nil for the zero value.

## RemoveErrDoesNotExist

Deleting something that is already gone is usually success, not failure. This
helper collapses that case:

```go
// nil if err is nil or wraps os.ErrNotExist, else err unchanged
func RemoveErrDoesNotExist(err error) error
```

`fs.RemoveFiles` and the recursive removal methods skip missing files the same
way.

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

| Condition                          | Check                                         |
| ---------------------------------- | --------------------------------------------- |
| File does not exist                | `errors.Is(err, os.ErrNotExist)`              |
| `MakeDir` on an existing path      | `errors.Is(err, os.ErrExist)`                 |
| `Move` onto an existing directory  | `errors.Is(err, os.ErrExist)`                 |
| Read or write of a directory       | `errors.Is(err, fs.ErrIsDirectory{})`         |
| A directory was expected           | `errors.Is(err, fs.ErrIsNotDirectory{})`      |
| `Remove` of a non-empty directory  | `errors.Is(err, fs.ErrDirectoryNotEmpty{})`   |
| `Remove` or `RemoveAll` of the root| `errors.Is(err, os.ErrInvalid)`               |
| Directory moved into itself        | `errors.Is(err, os.ErrInvalid)`               |
| Write on a read-only file system   | `errors.Is(err, fs.ErrReadOnlyFileSystem)`    |
| Read on a write-only file system   | `errors.Is(err, fs.ErrWriteOnlyFileSystem)`   |
| No file system for the URI         | `errors.Is(err, fs.ErrInvalidFileSystem)`     |
| Empty path                         | `errors.Is(err, fs.ErrEmptyPath)`             |
| Use after `Close`                  | `errors.Is(err, fs.ErrFileSystemClosed)`      |
| Backend can't do the operation     | `errors.Is(err, errors.ErrUnsupported)`       |
| Context cancelled                  | `errors.Is(err, context.Canceled)`            |

## Related

- [FileSystem interfaces](reference-filesystem-interfaces.md) — which operations can return `ErrUnsupported`
- [Serve files over HTTP](howto-serve-files-over-http.md)
- [Run the conformance suite](howto-run-the-conformance-suite.md) — the suite checks this contract
