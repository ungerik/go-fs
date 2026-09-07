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
URI prefix and a separator. Embed it and you are done with paths.

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
absolute paths starting with the separator; a non-rooted one (HTTP) has paths
that start with a host name.

Methods it provides:

| Method                                             | Returns                                        |
| -------------------------------------------------- | ---------------------------------------------- |
| `Prefix()`                                         | `URIPrefix`                                    |
| `PrefixAliases()`                                  | `AltPrefixes`                                  |
| `Separator()`                                      | `PathSep`, or `"/"` if empty                   |
| `CleanPath(uriParts ...string) string`             | The joined, cleaned, prefix-stripped path      |
| `TrimPrefix(uri string) string`                    | `uri` without `URIPrefix` or any `AltPrefixes` |
| `JoinCleanURI(uriParts ...string) string`          | `Prefix() + CleanPath(...)`                    |
| `URL(cleanPath string) string`                     | The URI of a clean path                        |
| `SplitDirAndName(filePath string) (dir, name string)` |                                                |
| `SplitPath(filePath string) []string`              | Path elements without prefix and volume        |
| `IsAbsPath(filePath string) bool`                  |                                                |
| `AbsPath(filePath string) string`                  |                                                |
| `IsHidden(filePath string) bool`                   | Name begins with a dot                         |

Embedding it satisfies the path half of `fs.FileSystem` and, if `AltPrefixes`
is set, `fs.PrefixAliasFileSystem` too:

```go
type myFS struct {
    fsimpl.PathHelper
    // ...
}

func newMyFS() *myFS {
    return &myFS{PathHelper: fsimpl.PathHelper{URIPrefix: "myfs://", Rooted: true}}
}
```

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

- Sequential reads stream the body opened for the current position.
- A `Seek` to another position closes the body, so the next `Read` opens a new
  one from there.
- `ReadAt` opens a body for exactly the requested range, independent of the
  sequential position.

Used by `webdavfs` and `azureblobfs` to implement seeking reads without
downloading the whole object.

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
        return resp.Body, nil
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
func NewFileBufferWithClose(data []byte, close func() error) *FileBuffer
func NewWriteOnCloseFileBuffer(data []byte, write func(data []byte) error) *FileBuffer
```

`ReadonlyFileBuffer` covers `io/fs.File`, `io.Reader`, `io.ReaderAt`,
`io.Seeker` and `io.Closer`, plus `Bytes`, `Size` and `Stat`. `FileBuffer`
embeds it and adds `Write`, `WriteAt` and `Truncate`, so it satisfies
`fs.ReadWriteSeekCloser`.

`NewWriteOnCloseFileBuffer` is the one that matters for backends: it hands the
complete buffer to your `write` callback on `Close`. That is exactly how the
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
`Close`. The `close` callback is optional and releases the underlying handle.

This is the fallback the package uses for `File.OpenReadWriter` when a backend
does not implement `fs.ReadWriterFileSystem`.

## DirTreeNode

A directory index built from the flat entry names of an archive, synthesizing
the implicit directories along the entry paths. `tarfs` uses it because a tar
archive has no directory index of its own.

```go
func NewDirTree() *DirTreeNode

type DirTreeNode struct {
    Path     string // slash separated, no leading slash
    Name     string
    IsDir    bool
    Size     int64
    Modified time.Time
}

func (n *DirTreeNode) Add(entryName string, modified time.Time, size int64) error
func (n *DirTreeNode) Lookup(path string) *DirTreeNode // nil if not found
func (n *DirTreeNode) SortedChildren() []*DirTreeNode // directories first
```

```go
tree := fsimpl.NewDirTree()
for _, entry := range archiveEntries {
    if err := tree.Add(entry.Name, entry.ModTime, entry.Size); err != nil {
        return err
    }
}
node := tree.Lookup("sub/deeper/leaf.md")
```

## Path functions

Standalone helpers, useful when embedding `PathHelper` does not fit.

| Function                                           | Behaviour                                          |
| -------------------------------------------------- | -------------------------------------------------- |
| `Ext(filePath, separator string) string`           | Extension including the dot. Unlike `path.Ext` the separator is a parameter; an **empty** separator searches the whole path, so points in directory elements count too. |
| `TrimExt(filePath, separator string) string`       | `filePath` without the extension.                  |
| `SplitDirAndName(filePath string, volumeLen int, separator string) (dir, name string)` | Handles a trailing separator correctly, unlike `path.Split`. Returns `"."` as dir when there is no separator before the name, and an empty name at the file system root. |
| `SplitPath(filePath, separator string) []string`   | Elements after trimming leading and trailing separators. `nil` if nothing is left. |
| `MatchAnyPattern(name string, patterns []string) (bool, error)` | `true` if `name` matches any pattern via `path.Match`, and `true` when `patterns` is empty. |
| `RandomString() string`                            | 120 random bits as a 20 character URL-safe base64 string, never starting with `-` or `_` so it can't be mistaken for a command line option. |

`Ext` is worth a second look because the empty-separator case is the surprise:

```go
fsimpl.Ext("image.png", "/")       // ".png"
fsimpl.Ext("image.png/file", "/")  // ""            like path.Ext: last element has no dot
fsimpl.Ext("image.png/file", "")   // ".png/file"   empty separator searches the whole path
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
`fs.ContentHashFuncFrom`:

```go
fs.DefaultContentHash = fs.ContentHashFuncFrom(sha256.New())
```

## Related

- [Implement a file system](tutorial-implement-a-filesystem.md) — these helpers in context
- [FileSystem interfaces](reference-filesystem-interfaces.md) — what to implement and what falls back
- [fstest reference](reference-fstest.md) — verify the result
