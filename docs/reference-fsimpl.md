# fsimpl

```go
import "github.com/ungerik/go-fs/fsimpl"
```

Helpers for writing a `fs.FileSystem`. Consumers of go-fs never need this
package; backend implementers use it so they do not re-derive path handling,
buffering and seeking for every backend.

Everything here is used by the backends that ship with go-fs, so the helpers
are the same code paths the conformance suite already exercises.

## PathHelper

Implements the path-related methods of `fs.FileSystem` for a file system with a
URI prefix and a separator.

```go
type PathHelper struct {
    URIPrefix   string              // "s3://bucket", "file://"
    AltPrefixes []string            // extra prefixes stripped like URIPrefix
    PathSep     string              // "/" if empty
    Rooted      bool                // paths are absolute (start with separator or volume)
    VolumeLen   func(path string) int // nil for file systems without volumes
}
```

The zero value is not usable — at minimum set `URIPrefix`.

`Rooted` is the one field worth thinking about. A rooted file system has
absolute paths starting with the separator; a non-rooted one has paths
that start with a host name.

A path is the output of `CleanPath`: cleaned, with the separator of the file
system and without the prefix. URL escapes are not decoded by `PathHelper`,
that is the job of `fs.ParseRawURI`. The URI of a path is the prefix followed
by the path escaped with `EscapeURIPath`, so a file named `a%20b` keeps its
name: its URI ends with `a%2520b`.

Methods it provides:

| Method                                             | Returns                                        |
| -------------------------------------------------- | ---------------------------------------------- |
| `Prefix()`                                         | `URIPrefix`                                    |
| `PrefixAliases()`                                  | `AltPrefixes`                                  |
| `Separator()`                                      | `PathSep`, or `"/"` if empty                   |
| `CleanPath(uriParts ...string) string`             | The joined, cleaned, prefix-stripped path      |
| `TrimPrefix(uri string) string`                    | `uri` without the longest matching `URIPrefix` or `AltPrefixes` |
| `URL(cleanPath string) string`                     | `URIPrefix` + `EscapeURIPath(cleanPath)`       |
| `JoinCleanURI(uriParts ...string) string`          | `URL(CleanPath(uriParts...))`                  |
| `SplitDirAndName(filePath string) (dir, name string)` | Parent dir and name, prefix and volume aware |
| `SplitPath(filePath string) []string`              | Path elements without prefix and volume        |

`URL` does not double the separator when `URIPrefix` ends with one after a
host, like `mem://id/`. A prefix that is only a scheme is followed by the whole
rooted path, so with `URIPrefix: "file://"` the URI of `/a` is `file:///a`.

`SplitDirAndName` and `SplitPath` both accept a path with or without the
prefix. The parent of a name directly below a volume root is the volume root:
`C:\a` splits into `C:\` and `a`, and `C:\` itself has an empty name.

Embedding it satisfies the path half of `fs.FileSystem` and, because
`PrefixAliases` is always there, `fs.PrefixAliasFileSystem` too; it returns
nil when `AltPrefixes` is empty, which the package treats as no aliases:

```go
type myFS struct {
    fsimpl.PathHelper
    // ...
}

func newMyFS() *myFS {
    return &myFS{PathHelper: fsimpl.PathHelper{URIPrefix: "myfs://host", Rooted: true}}
}
```

Embed it in an unexported type, or keep it in an unexported field and forward
the methods, like the file systems of go-fs do. Embedded in an exported type
its fields become mutable API of that type. Methods added to `PathHelper` never
complete an optional interface of package `fs`, so embedding it can't make a
file system implement `fs.HiddenFileSystem` or `fs.AbsPathFileSystem` by
accident.

File systems whose paths are URLs in escaped form, where `?` starts a query
that must not be cleaned, don't fit `PathHelper`. They implement
`fs.URIFileSystem` instead, like `httpfs`.

## RangeReader

An `io.ReadSeekCloser` and `io.ReaderAt` for remote files read with byte range
requests: HTTP `GET` with a `Range` header, object store downloads.

```go
type RangeReader struct {
    Size int64
    Open func(offset, count int64) (io.ReadCloser, error)
}
```

`Open` returns the content starting at `offset`, limited to `count` bytes if
`count` is not negative. The returned body **must be positioned at `offset`**
even if the server ignored the requested range.

Behaviour:

- Sequential reads stream the body opened for the current position and never
  read beyond `Size`.
- A `Seek` to another position closes the body, so the next `Read` opens a new
  one from there.
- `ReadAt` opens a body for exactly the requested range, independent of the
  sequential position.
- A body that ends before `Size`, like after a dropped connection, is
  `io.ErrUnexpectedEOF`, not the end of the file. The next `Read` opens a new
  body from the current position.
- Reads after `Close` return `os.ErrClosed`.

Used by `webdavfs` and `azureblobfs` to implement seeking reads without
downloading the whole object.

`Open` has to check the status code: a `206 Partial Content` body starts at
`offset`, a `200 OK` means the server ignored the range and sends the whole
file, and anything else is an error, not content. This is `ExampleRangeReader`
in the package:

```go
r := &fsimpl.RangeReader{
    Size: size,
    Open: func(offset, count int64) (io.ReadCloser, error) {
        req, err := http.NewRequest(http.MethodGet, url, nil)
        if err != nil {
            return nil, err
        }
        if count < 0 {
            req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
        } else {
            req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+count-1))
        }
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
            return nil, err
        }
        switch resp.StatusCode {
        case http.StatusPartialContent:
            return resp.Body, nil
        case http.StatusOK:
            // The server ignored the range and sends the whole file
            if _, err = io.CopyN(io.Discard, resp.Body, offset); err != nil {
                return nil, errors.Join(err, resp.Body.Close())
            }
            return resp.Body, nil
        default:
            return nil, errors.Join(fmt.Errorf("unexpected status %s", resp.Status), resp.Body.Close())
        }
    },
}
defer r.Close()
```

## FileBuffer and ReadonlyFileBuffer

In-memory buffers that implement the `io` interfaces a file needs.

```go
func NewReadonlyFileBuffer(data []byte, info iofs.FileInfo) *ReadonlyFileBuffer
func NewReadonlyFileBufferReadAll(reader io.Reader, info iofs.FileInfo) (*ReadonlyFileBuffer, error)

func NewFileBuffer(data []byte) *FileBuffer
func NewWriteOnCloseFileBuffer(data []byte, write func(data []byte) error) *FileBuffer
```

`ReadonlyFileBuffer` covers `io/fs.File`, `io.Reader`, `io.ReaderAt`,
`io.Seeker` and `io.Closer`, plus `Bytes`, `Size` and `Stat`. `FileBuffer`
embeds it and adds `Write`, `WriteAt`, `Truncate` and `Abort`, so it satisfies
`fs.ReadWriteSeekCloser` and `fs.WriteAborter`.

After `Close` (or `Abort`) every method except `Stat`, `Bytes` and `Size`
returns `os.ErrClosed`, like an `*os.File`, so data written after `Close` is
an error instead of being dropped silently. `Close` is idempotent.

`NewWriteOnCloseFileBuffer` is the one that matters for backends: it hands the
complete buffer to your `write` callback on `Close`. `Abort` discards the
buffer without calling `write`, so the package can cancel an upload that
failed midway instead of committing the partial data. That is exactly how the
package emulates `OpenAppendWriter` for backends without a native append.

```go
// An append writer for a backend that can only write whole files
func (f *myFS) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
    current, err := f.readWholeFile(filePath)
    if err != nil && !errors.Is(err, os.ErrNotExist) {
        return nil, err
    }
    buf := fsimpl.NewWriteOnCloseFileBuffer(current, func(data []byte) error {
        return f.writeWholeFile(filePath, data, perm)
    })
    _, err = buf.Seek(0, io.SeekEnd) // writes must append
    return buf, err
}
```

## ReadWriteAllSeekCloser

Random access over a backend that can only read and write whole files, such as
a ZIP archive.

```go
func NewReadWriteAllSeekCloser(
    readAll func() ([]byte, error),
    writeAll func([]byte) error,
    close func() error,
) *ReadWriteAllSeekCloser
```

It lazily reads the whole file on the first read or write, keeps every
operation in memory in a `FileBuffer`, and writes the complete content back on
`Close`. The `close` callback is optional and releases the underlying handle;
it runs on `Close` and on `Abort`, at most once. `Abort` discards the
modifications without writing them back, implementing `fs.WriteAborter`.
After `Close` or `Abort` every method returns `os.ErrClosed`.

This is the fallback the package uses for `File.OpenReadWriter` when a backend
does not implement `fs.ReadWriterFileSystem`.

## Path functions

Standalone helpers, useful when embedding `PathHelper` does not fit.

| Function                                           | Behaviour                                          |
| -------------------------------------------------- | -------------------------------------------------- |
| `Ext(filePath, separator string) string`           | Extension including the dot. Unlike `path.Ext` the separator is a parameter; an **empty** separator searches the whole path, so points in directory elements count too. |
| `TrimExt(filePath, separator string) string`       | `filePath` without the extension.                  |
| `SplitDirAndName(filePath string, volumeLen int, separator string) (dir, name string)` | Handles a trailing separator correctly, unlike `path.Split`. Returns `"."` as dir when there is no separator before the name, and an empty name at the root of the file system or of a volume. The first `volumeLen` bytes are the volume: `C:\a` splits into `C:\` and `a`. |
| `SplitPath(filePath, separator string) []string`   | Elements after trimming leading and trailing separators. `nil` if nothing is left. |
| `EscapeURIPath(filePath string) string`            | `filePath` for the path part of a URI: every `%` becomes `%25`, nothing else changes. |
| `UnescapeURIPath(uriPath string) string`           | Decodes the URL escapes of a URI path, the inverse of `EscapeURIPath`. An invalid escape like `50% off` returns the input unchanged. |
| `MatchAnyPattern(name string, patterns []string) (bool, error)` | `true` if `name` matches any pattern via `path.Match`, and `true` when `patterns` is empty. |
| `RandomString() string`                            | 120 random bits as a 20 character URL-safe base64 string, never starting with `-` or `_` so it can't be mistaken for a command line option. |

`Ext` is worth a second look because the empty-separator case is the surprise:

```go
fsimpl.Ext("image.png", "/")       // ".png"
fsimpl.Ext("image.png/file", "/")  // ""            like path.Ext: last element has no dot
fsimpl.Ext("image.png/file", "")   // ".png/file"   empty separator searches the whole path
```

## Password redaction

```go
func RedactURI(uri string) string
func RedactAddress(address string) string
```

Both return their argument for error messages and logs with the password of
the user info replaced by `xxxxx`, also if `url.Parse` rejects it. The `fs`
package, `httpfs`, `s3fs`, `sftpfs`, `ftpfs`, `smbfs` and `webdavfs` use them,
so every package redacts the same way.

`RedactURI` is for URIs whose path may contain an `@`, like the URI of a file.
The user info ends with the last `@` of the authority, unless a password with
an unencoded `/`, `?` or `#` moved its end behind the authority: then the last
`@` of the URI ends it. That is the case if the host or port is invalid, like
`user:secret` of `ftp://user:secret/x@host`, or if the authority has no user
info and looks like a host with a port, like `u:123` of `ftp://u:123/x@host`,
which `url.Parse` takes for the host `u` with the port `123`. A URI without
`://` is returned unchanged. The doc comment of `RedactURI` lists every rule.

`RedactAddress` is for the address of a file system that is rejected because
it has an `@` after the host: everything up to the last `@` is the user info.
It also redacts an address without scheme like `user:secret@host`.

```go
fsimpl.RedactURI("ftp://user:secret@host/me@example.com.txt") // "ftp://user:xxxxx@host/me@example.com.txt"
fsimpl.RedactURI("ftp://u:123/x@host/dir")                    // "ftp://u:xxxxx@host/dir"
fsimpl.RedactAddress("sftp://bob:a@b/c@host")                 // "sftp://bob:xxxxx@host"
```

## Content hashing

```go
func DropboxContentHash(ctx context.Context, reader io.Reader) (string, error)
```

The Dropbox content hash algorithm. It is the value of `fs.DefaultContentHash`,
so `File.ContentHash` and `FileReader.ContentHash` return it unless you replace
that variable. Choosing it as the default means `dropboxfs` can answer
`ContentHash` from metadata instead of downloading the file.

To use a standard hash instead, set `fs.DefaultContentHash` with
`fs.ContentHashFuncFrom`, which creates a new hash for every call:

```go
fs.DefaultContentHash = fs.ContentHashFuncFrom(sha256.New)
```

## Related

- [Implement a file system](tutorial-implement-a-filesystem.md) — these helpers in context
- [FileSystem interfaces](reference-filesystem-interfaces.md) — what to implement and what falls back
- [fstest reference](reference-fstest.md) — verify the result
