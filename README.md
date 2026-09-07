go-fs: A unified file system for Go
===================================

[![Go Reference](https://pkg.go.dev/badge/github.com/ungerik/go-fs.svg)](https://pkg.go.dev/github.com/ungerik/go-fs)
[![Go Report Card](https://goreportcard.com/badge/github.com/ungerik/go-fs)](https://goreportcard.com/report/github.com/ungerik/go-fs)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ungerik/go-fs)](https://github.com/ungerik/go-fs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub release](https://img.shields.io/github/release/ungerik/go-fs.svg)](https://github.com/ungerik/go-fs/releases)

The package is built around a `File` type that is a string underneath
and interprets its value as a local file system path or as a URI.

Version 1.0 is in beta; the v1 API is being iterated in beta releases
before it is frozen. Upgrading from v0.x is mechanical, see
[docs/MIGRATION_v1.md](docs/MIGRATION_v1.md).

Documentation
-------------

The rest of this README is an API tour. For task-oriented guides,
design rationale and the implementer reference see **[docs/](docs/README.md)**.

*Tutorials*

- [Getting started](docs/tutorial-getting-started.md) — read and write files
  against local disk, memory and HTTP with the same code
- [Implement a file system](docs/tutorial-implement-a-filesystem.md) — a
  complete backend in ~120 lines that passes the conformance suite

*How-to*

- [Connect a remote backend](docs/howto-connect-a-remote-backend.md) — S3,
  SFTP, FTP, SMB, WebDAV, Azure Blob, Dropbox
- [Test with MemFileSystem](docs/howto-test-with-memfilesystem.md)
- [Serve files over HTTP](docs/howto-serve-files-over-http.md)
- [Handle form uploads](docs/howto-handle-form-uploads.md)
- [Work with archives](docs/howto-work-with-archives.md)
- [Copy, move and compare](docs/howto-copy-move-and-compare.md)
- [Run the conformance suite](docs/howto-run-the-conformance-suite.md)
- [Migrating to v1.0](docs/MIGRATION_v1.md)

*Reference*

- [FileSystem interfaces](docs/reference-filesystem-interfaces.md) — all 28
  interfaces, every fallback, the backend support matrix
- [Errors](docs/reference-errors.md) · [fsimpl](docs/reference-fsimpl.md) ·
  [fstest](docs/reference-fstest.md) · [uuiddir](docs/reference-uuiddir.md)
- Full API on [pkg.go.dev](https://pkg.go.dev/github.com/ungerik/go-fs)

*Explanation*

- [Why File is a string](docs/explanation-the-file-type.md) ·
  [Paths, URIs and the registry](docs/explanation-path-and-uri-model.md) ·
  [The context rule](docs/explanation-context-rule.md) ·
  [Optional interfaces and emulation](docs/explanation-optional-interfaces.md)

Introduction
------------

`FileSystem` implementations can be registered with their
URI qualifiers like `file://` or `http://`.

The methods of `File` parse their string value for a qualifier
and look up a `FileSystem` in the `Registry`.
The only special rule is, that if no qualifier is present,
then the string value is interpreted as a local file path.

The `LocalFileSystem` is registered by default.

Work with `Local` directly:

```go
fs.Local.Separator() // Either `/` or `\`

fs.Local.IsSymbolicLink("~/file") // Tilde expands to user home dir
```

For example, create a `FileSystem` from a multi-part
HTTP form request that contains an uploaded file: 

```go
import "github.com/ungerik/go-fs/multipartfs"

multipartFS, err := multipartfs.FromRequestForm(request, MaxUploadSize)

defer multipartFS.Close()

// Access form values as string
multipartFS.FormValue("email")

// Access form files as fs.File
file, err := multipartFS.FormFile("file")

// Use like any other fs.File
bytes, err := file.ReadAll(ctx)
```

fs.File
-------

```go
type File string
```

As a string-type it's easy to assign string literals and it can be const
which would be impossible if `File` was an interface or struct:

```go
const fileConst fs.File = "~/file.a"

var fileVar fs.File = "~/file.b"

fileVar = fileConst
fileVar = "~/file.c"
```

Handy to pass string literals of local paths or URIs to functions:

```go
func readFile(f fs.File) { /* ... */ }

readFile("../my-local-file.txt")

// HTTP reading works when httpfs is imported
import _ "github.com/ungerik/go-fs/httpfs"

readFile("https://example.com/file-via-uri.txt")
```

As a string type `File` naturally marshals/unmarshals as string path/URI
without having to implement marshaling interfaces.

But it implements `fmt.Stringer` to add the name of the path/URI filesystem
as debug information.

Path related methods:

```go
file := fs.TempDir().Join("file.txt")

dir := file.Dir()   // "/tmp" == fs.TempDir()
name := file.Name() // "file.txt"
path := file.Path() // "/tmp/file.txt"
url := file.URL()   // "file:///tmp/file.txt"
ext := file.Ext()   // ".txt"
lower := file.ExtLower() // ".txt"
trimmed := file.TrimExt() // "/tmp/file"

slashed := file.PathWithSlashes() // forward slashes regardless of OS
local := file.LocalPath()         // "" if not on Local
mustLocal := file.MustLocalPath() // panics if not on Local

file2 := file.Dir().Join("a", "b", "c").Joinf("file%d.txt", 2)
path2 := file2.Path() // "/tmp/a/b/c/file2.txt"

abs := fs.File("~/some-dir/../file").AbsPath() // "/home/erik/file"
isAbs := abs.HasAbsPath()                      // true
absFile := fs.File("relative/path").ToAbsPath() // File with an absolute path

// Relative path between two files on the same FileSystem
base := fs.File("/tmp/project")
rel, err := base.RelPathOf(base.Join("a", "b", "c")) // "a/b/c"
```

Access and existence checks:

```go
file.IsReadable() // file exists and is readable
file.IsWritable() // file (or its parent dir) is writable
file.IsEmptyDir() // directory exists and contains no entries
```

Ownership (where the file system supports it, e.g. `LocalFileSystem`):

```go
user, err := file.User()
err = file.SetUser("alice")

group, err := file.Group()
err = file.SetGroup("staff")
```

Resizing existing files (where supported):

```go
err := file.Truncate(ctx, 1024) // resize to exactly 1024 bytes
```

Meta information:

```go
size := file.Size() // int64, 0 for non existing or dirs
isDir := dir.IsDir()      // true
exists := file.Exists()   // true
fileIsDir := file.IsDir() // false
modTime := file.Modified()
hash, err := file.ContentHash(ctx)  // Dropbox hash algo
regular := file.Info().IsRegular // true
info := file.Info().FSFileInfo() // io/fs.FileInfo
```

Reading and writing files
-------------------------

Reading:

```go
bytes, err := file.ReadAll(ctx)

str, err := file.ReadAllString(ctx)

var w io.Writer
n, err := file.WriteTo(w)

f, err := file.OpenReader()     // io/fs.File 
r, err := file.OpenReadSeeker() // fs.ReadSeekCloser

```

Writing:

```go
err := file.WriteAll(ctx, []byte("Hello"))

err := file.WriteAllString(ctx, "Hello")

err := file.Append(ctx, []byte("Hello"))

err := file.AppendString(ctx, "Hello")

var r io.Reader
n, err := file.ReadFrom(r)

w, err := file.OpenWriter()       // io.WriteCloser
w, err := file.OpenAppendWriter() // io.WriteCloser

rw, err := file.OpenReadWriter() // fs.ReadWriteSeekCloser
```

fs.FileReader
-------------

For cases where a file should be passed only for reading,
it's recommended to use the interface type `FileReader`.
It has all the read-related methods of `File`, so a `File` can be assigned
or passed as `FileReader`:

```go
type FileReader interface { /* ... */ }
```

```go
func readFile(f fs.FileReader) { /* ... */ }

// An untyped string literal does not work as interface,
// needs a concrete type like fs.File
readFile(fs.File("../my-local-file.txt"))
```

fs.MemFile
----------

`MemFile` combines the buffered in-memory data of a file
with a filename to implement fs.FileReader.
It exposes `FileName` and `FileData` as exported struct fields to emphasize
its simple nature as just a wrapper of a name and some bytes.

```go
type MemFile struct {
	FileName string
	FileData []byte
}
```

**Pass by value:** `MemFile` should be passed by value (not by pointer) because it's a small,
simple struct containing only a string and a slice (both reference types internally).
Passing by value is more efficient and idiomatic for such lightweight types.
This is why `NewMemFile` returns a `MemFile` value, not a pointer.

The type exists because it's very common to build up a file in memory
and/or pass around some buffered file bytes together with a filename:

```go
func readFile(f fs.FileReader) { /* ... */ }

readFile(fs.NewMemFile("hello-world.txt", []byte("Hello World!")))

// Read another fs.FileReader into a fs.MemFile
// to have it buffered in memory
memFile, err := fs.ReadMemFile(ctx, fs.File("../my-local-file.txt"))

// Read all data similar to io.ReadAll from an io.Reader
var r io.Reader
memFile, err := fs.ReadAllMemFile(ctx, r, "in-mem-file.txt")
```

Note that `MemFile` is not a `File` because it has no backing file system:
its `FileName` is just a name, not a registered path or URI.

The `FileName` can mirror the complete path of a file or directory on a
file system, but the most common simple case is just a name without any
slashes and path semantics. When the `FileName` contains slashes, the path
methods interpret it as a `/`-separated path (a backslash is an ordinary
character, not a separator). A `FileName` ending with a slash marks the
`MemFile` as a directory: `IsDir` returns true, `FileData` is ignored, and
the read methods return an `ErrIsDirectory` error.

```go
mf := fs.NewMemFile("some/path/file.txt", data)

mf.Name()                    // "file.txt"
mf.Ext()                     // ".txt"
mf.Dir()                     // MemFile{FileName: "some/path/"}, a directory
dir, name := mf.DirAndName() // MemFile{FileName: "some/path/"}, "file.txt"

mf.IsDir()                                   // false
fs.MemFile{FileName: "some/path/"}.IsDir()   // true
fs.MemFile{FileName: "a/b/../c"}.CleanPath() // "a/c"
```

Derive new `MemFile` values without copying the underlying data:

```go
renamed := memFile.WithName("renamed.txt")  // same FileData, different name
patched := memFile.WithData(newBytes)       // same FileName, different data
```

`WithName` replaces the whole `FileName` (like `WithData` replaces the whole `FileData`) including any path,
so it is not symmetric with `Name` which only returns the last path element.

Listing directories
-------------------

Callback-based listing (cancel by returning an error from the callback or
canceling the context):

```go
// Print names of all entries in dir
dir.ListDir(ctx, func(f fs.File) error {
	_, err := fmt.Println(f.Name())
	return err
})

// Print names of all JPEGs in dir and all recursive sub-dirs
dir.ListDirRecursive(ctx, func(f fs.File) error {
	_, err := fmt.Println(f.Name())
	return err
}, "*.jpg", "*.jpeg")

// Get all files in dir without limit
files, err := dir.ListDirMax(ctx, -1)

// Get the first 100 JPEGs in dir
files, err := dir.ListDirMax(ctx, 100, "*.jpg", "*.jpeg")

// Recursive variant with a hard cap
files, err := dir.ListDirRecursiveMax(ctx, 1000, "*.go")
```

Go 1.23+ iterator methods (`iter.Seq2[fs.File, error]`):

```go
// Range directly over directory entries
for file, err := range dir.ListDirIter(ctx, "*.jpg", "*.jpeg") {
	if err != nil {
		return err
	}
	fmt.Println(file.Name())
}

// Recursive iteration with a cancelable context
for file, err := range dir.ListDirRecursiveIter(ctx, "*.go") {
	if err != nil {
		return err
	}
	fmt.Println(file.Path())
}
```

Glob with wildcard substitution (Go 1.23+ iterator, the second yielded
value is the list of substituted wildcard segments):

```go
// All Go files under any "cmd/*" sub-directory
for file, segments := range fs.MustGlob(ctx, "cmd/*/*.go") {
	fmt.Println(segments, file.Path()) // segments == ["mytool", "main.go"]
}

// Relative to a specific base directory
iter, err := dir.Glob(ctx, "**/*.png")
if err != nil {
	return err
}
for file := range iter {
	fmt.Println(file.Path())
}
```

A pattern ending with a slash (`/`) only matches directories. Glob
ignores I/O errors and only fails on a malformed pattern.

Standard library compatibility (`io/fs`)
----------------------------------------

A `File` can be exposed as an `io/fs.FS` so it works with any code that
expects the standard library file system abstraction (`fs.WalkDir`,
`html/template.ParseFS`, `http.FS`, ...):

```go
stdFS := fs.File("/srv/static").StdFS()

// Use with io/fs
entries, err := iofs.ReadDir(stdFS, ".")

// Or with http.FS (note: http.FS expects io/fs.FS)
http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(stdFS))))
```

`StdFS` implements `io/fs.FS`, `io/fs.SubFS`, `io/fs.StatFS`,
`io/fs.ReadDirFS`, and `io/fs.ReadFileFS`.

A `File` can also be returned as a directory entry:

```go
entry := fs.File("./file.txt").StdDirEntry() // io/fs.DirEntry
```

JSON and XML helpers
--------------------

```go
type Config struct { Name string `json:"name"` }

var cfg Config
err := fs.File("config.json").ReadJSON(ctx, &cfg)
err = fs.File("config.json").WriteJSON(ctx, &cfg, "  ") // indented

err = fs.File("config.xml").ReadXML(ctx, &cfg)
err = fs.File("config.xml").WriteXML(ctx, &cfg, "  ")
```

The same helpers are also available as `MemFile` constructors:

```go
m, err := fs.NewMemFileWriteJSON("config.json", &cfg, "  ")
m, err  = fs.NewMemFileWriteXML("config.xml", &cfg, "  ")
```

Symbolic links
--------------

File systems opt into symbolic link support by implementing the
`SymbolicLinkFileSystem` interface. `LocalFileSystem`, `MemFileSystem`,
`sftpfs` and `smbfs` opt in; the other backends do not, so calling these
methods on files from those backends returns an `ErrUnsupported` error, and
`IsSymbolicLink` is false.

```go
target := fs.File("/etc/hosts")
link := fs.File("/tmp/hosts-link")

// Create link as a symbolic link pointing to target
err := link.CreateSymbolicLink(target)

// Check whether a file is a symbolic link
if link.IsSymbolicLink() {
    // Read the link target as stored on disk (may be relative)
    resolved, err := link.ReadSymbolicLink()
    _ = resolved
}
```

Both files must live on the same `FileSystem`; passing a target from a
different file system returns an error. The target path is stored verbatim,
so pass an absolute path if you want the link to resolve the same way
regardless of where it is read from.

Watching the local file system
------------------------------

The local file system supports watching files and directories for changes using the `fsnotify` package.
This functionality is available through the `WatchFileSystem` interface and the `File.Watch` method.

```go
// Watch a directory for changes
dir := fs.File("/path/to/watch")

cancel, err := dir.Watch(func(file fs.File, event fs.Event) {
    if event.HasCreate() {
        fmt.Printf("File created: %s\n", file.Name())
    }
    if event.HasWrite() {
        fmt.Printf("File written: %s\n", file.Name())
    }
    if event.HasRemove() {
        fmt.Printf("File removed: %s\n", file.Name())
    }
    if event.HasRename() {
        fmt.Printf("File renamed: %s\n", file.Name())
    }
    if event.HasChmod() {
        fmt.Printf("File permissions changed: %s\n", file.Name())
    }
})

if err != nil {
    log.Fatal(err)
}

// Later, cancel the watch
defer cancel()
```

**Event Types:**
- `HasCreate()` - File or directory was created
- `HasWrite()` - File was written to
- `HasRemove()` - File or directory was removed
- `HasRename()` - File or directory was renamed
- `HasChmod()` - File permissions were changed

**Important Notes:**
- Watching a directory only reports changes directly within it, not in recursive sub-directories
- Multiple watches can be set on the same file/directory
- The returned `cancel` function stops a specific watch
- Events are delivered asynchronously via goroutines
- The local filesystem supports watching; other filesystems may not

**Logging:**
You can enable logging for watch events and errors:

```go
fs.Local.WatchEventLogger = fs.LoggerFunc(func(format string, args ...any) {
    log.Printf("WATCH: "+format, args...)
})

fs.Local.WatchErrorLogger = fs.LoggerFunc(func(format string, args ...any) {
    log.Printf("WATCH ERROR: "+format, args...)
})
```

File system implementations
---------------------------

`go-fs` ships with the local file system and several remote / virtual
backends. The network backends (`s3fs`, `azureblobfs`, `sftpfs`, `ftpfs`,
`smbfs`, `dropboxfs`, `webdavfs`) are independent Go modules under their own
sub-directory with their own dependencies; the lighter ones (`httpfs`,
`zipfs`, `tarfs`, `multipartfs`) are packages in the root module. Importing
the package registers a `FileSystem` for its URI prefix, after which `File`
values with that prefix transparently route to the right backend.

| Package       | URI prefix              | Constructor                                        | Read | Write  |
| ------------- | ----------------------- | -------------------------------------------------- | :--: | :----: |
| (built-in)    | `file://` or none       | `fs.Local` (registered by default)                 | yes  | yes    |
| `httpfs`      | `http://`, `https://`   | side-effect import: `import _ ".../httpfs"`        | yes  | no     |
| `s3fs`        | `s3://<bucket>`         | `s3fs.NewAndRegister`, `s3fs.NewLoadDefaultConfig` | yes  | yes/ro |
| `sftpfs`      | `sftp://<user>@<host>`  | `sftpfs.Dial`, `DialAndRegister`, `EnsureRegistered` | yes | yes  |
| `ftpfs`       | `ftp://`, `ftps://`     | `ftpfs.Dial`, `DialAndRegister`, `EnsureRegistered` | yes | yes   |
| `dropboxfs`   | `dropbox://<account>`   | `dropboxfs.NewAndRegister`                         | yes  | yes    |
| `webdavfs`    | `webdav://<host>/<base>` | `webdavfs.New`, `webdavfs.NewAndRegister`         | yes  | yes    |
| `smbfs`       | `smb://<user>@<host>/<share>` | `smbfs.Dial`, `smbfs.DialAndRegister`         | yes  | yes    |
| `azureblobfs` | `azblob://<host>/<container>` | `azureblobfs.NewAndRegister`, `NewFromConnectionString` | yes | yes/ro |
| `zipfs`       | `zip://`                | `zipfs.NewReader`, `zipfs.NewWriter`               | reader | writer |
| `tarfs`       | `tar://`                | `tarfs.NewReader`, `tarfs.NewWriter`               | reader | writer |
| `multipartfs` | `multipart://`          | `multipartfs.FromRequestForm`                      | yes  | no     |
| (built-in)    | `mem://`                | `fs.NewMemFileSystem`                              | yes  | yes    |
| (built-in)    | `stdfs://<id>`          | `fs.NewStdFileSystem(iofs.FS)`: embed.FS, os.DirFS, zip.Reader, MapFS | yes | no |
| (built-in)    | `sub://<id>`            | `fs.NewSubFileSystem(parent, dir)`: view rooted at a directory | as parent | as parent |
| (built-in)    | `overlay://<id>`        | `fs.NewOverlayFileSystem(base, upper)`: writable layer over a read-only base | yes | yes |

Every registered file system has a stable `ID()`: the file system id of the
root volume for the local file system, the bucket for s3fs, `user@host` for
sftpfs and ftpfs, the account id for dropboxfs, and the host with the base
path, share or container for webdavfs, smbfs and azureblobfs.

### Optional interface support

Every backend implements the core `FileSystem` interface. Beyond that, the
package defines small *optional* interfaces (`CopyFileSystem`, `MoveFileSystem`,
`TouchFileSystem`, …). When a backend does not implement one, the operation
still works through a generic emulation built on the core methods — a backend
only implements an optional interface when doing so is more efficient or more
capable than that emulation.

`LocalFileSystem` and `MemFileSystem` implement almost every optional
interface natively; what they leave to the emulation is what the emulation
already does best (`Exists` is a `Stat`, and the local file system has no
recursive listing faster than the generic walk). `MemFileSystem` additionally
implements `User`/`Group`, which the local file system only exposes on Unix.
For the remote backends, as verified by the conformance suite in `fstest`:

| Capability             | s3  | azblob | sftp | ftp | smb | dropbox | webdav | http |
| ---------------------- | :-: | :----: | :--: | :-: | :-: | :-----: | :----: | :--: |
| CopyFile (server-side) | ✓   | ✓      | –    | –   | –   | ✓       | ✓      | –    |
| Move                   | –   | –      | ✓    | ✓   | ✓   | ✓       | ✓      | –    |
| Exists                 | –   | –      | –    | –   | –   | –       | –      | ✓    |
| ReadAll                | ✓   | ✓      | –    | ✓   | ✓   | –       | ✓      | ✓    |
| WriteAll               | ✓   | ✓      | –    | ✓   | ✓   | ✓       | ✓      | r/o  |
| Append                 | –   | –      | –    | ✓   | –   | –       | –      | r/o  |
| OpenAppendWriter       | –   | –      | ✓    | ✓   | ✓   | –       | –      | r/o  |
| OpenReadWriter         | ✓   | ✓      | ✓    | ✓   | ✓   | ✓       | –      | r/o  |
| Touch                  | ✓   | ✓      | ✓    | ✓   | ✓   | –       | –      | r/o  |
| Truncate               | –   | –      | ✓    | –   | ✓   | –       | –      | r/o  |
| MakeAllDirs            | –   | –      | ✓    | –   | ✓   | –       | –      | r/o  |
| RemoveAll              | ✓   | ✓      | ✓    | ✓   | ✓   | ✓       | ✓      | r/o  |
| ListDirRecursive       | ✓   | ✓      | ✓    | ✓   | –   | ✓       | –      | –    |
| SetPermissions         | –   | –      | ✓    | –   | ✓   | –       | –      | r/o  |
| Symbolic links         | –   | –      | ✓    | –   | ✓   | –       | –      | –    |
| Seeking reads          | –   | ✓      | ✓    | –   | ✓   | –       | ✓      | –    |

`✓` native implementation · `–` falls back to the generic emulation (works the
same, just not specialized) · `r/o` read-only backend, so the write operation
does not apply.

The archive and request-scoped backends implement a mode-dependent subset:
`zipfs.Reader` is a `StdFileSystem` and provides `ReadAll` and
`ListDirRecursive`, `tarfs.Reader` provides `Exists` and `ListDirRecursive`,
the writers provide `Touch` (`tarfs.Writer` also `WriteAll`), and
`multipartfs` is read-only and provides `Exists` and `ReadAll`.

Every backend follows the same error contract: `errors.Is(err, os.ErrNotExist)`
for missing files, `os.ErrExist` for `MakeDir` on an existing path,
`fs.ErrReadOnlyFileSystem` / `fs.ErrWriteOnlyFileSystem` for the wrong
direction, `fs.ErrFileSystemClosed` after `Close`, and the context error when
a context is cancelled. Methods only take a `context.Context` when they can
take long: reading or writing content, listing directories and dialing
connections.

### httpfs

```go
import _ "github.com/ungerik/go-fs/httpfs"

data, err := fs.File("https://example.com/file.txt").ReadAll(ctx)
```

Read-only. Useful for treating remote files uniformly with local ones.
`httpfs.Client` is the `*http.Client` used for all requests.

### s3fs

```go
import "github.com/ungerik/go-fs/s3fs"

// Using the default AWS credential chain
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", false)

// Or with an existing aws-sdk-go-v2 client
bucket = s3fs.NewAndRegister(client, "my-bucket", false)

err = fs.File("s3://my-bucket/path/file.txt").WriteAllString(ctx, "Hello")
```

Multipart upload/download is used automatically for files larger than
`s3fs.MultipartUploadThreshold` / `s3fs.MultipartDownloadThreshold`
(5 MB / 10 MB). Directories are object key prefixes; `MakeDir` creates a
zero-byte marker object so empty directories exist too.
[s3fs/README.md](s3fs/README.md) has the credential setup, the
S3-compatible service configuration and the full concept mapping.

### sftpfs

```go
import "github.com/ungerik/go-fs/sftpfs"

sftpFS, err := sftpfs.DialAndRegister(
    ctx,
    "sftp://user@host",
    sftpfs.Password("secret"),
    knownhosts.New("~/.ssh/known_hosts"), // ssh.HostKeyCallback, or sftpfs.AcceptAnyHostKey
    nil,                                  // optional fs.Logger for connection events
)
defer sftpFS.Close()

data, err := fs.File("sftp://user@host/etc/hostname").ReadAll(ctx)
```

A lost connection is re-dialed transparently. `EnsureRegistered` shares one
connection per address between callers with reference counting. URIs with
embedded credentials (`sftp://user:password@host/path`) dial a connection per
operation and require `sftpfs.URLHostKeyCallback` to be set.

### ftpfs

```go
import "github.com/ungerik/go-fs/ftpfs"

ftpFS, err := ftpfs.DialAndRegister(
    ctx,
    "ftps://example.com",
    ftpfs.UsernameAndPassword("user", "secret"),
    nil, // *ftpfs.Options: TLS config, InsecureSkipVerify, protocol debug output
)
defer ftpFS.Close()
```

`ftp://` is plain FTP, `ftps://` is explicit TLS on port 21 (implicit TLS
with port 990). Server certificates are verified unless
`ftpfs.Options.InsecureSkipVerify` is set. The single control connection is
used by one operation at a time; `OpenReader` streams over its own connection.

### dropboxfs

```go
import "github.com/ungerik/go-fs/dropboxfs"

dbxFS, err := dropboxfs.NewAndRegister(ctx, accessToken, 5*time.Minute, false)
defer dbxFS.Close()

// The prefix is dropbox://<account id>, so it is stable per account
err = dbxFS.RootDir().Join("Apps", "MyApp", "notes.md").WriteAllString(ctx, "...")
```

The second argument is the metadata cache timeout (zero disables the cache),
the third mutes the notifications Dropbox sends for changed files.

### webdavfs

```go
import "github.com/ungerik/go-fs/webdavfs"

davFS, err := webdavfs.NewAndRegister(ctx, "https://cloud.example.com/remote.php/dav/files/alice",
    &webdavfs.Options{Username: "alice", Password: "app-password"})
defer davFS.Close()

notes, err := fs.File("webdav://cloud.example.com/remote.php/dav/files/alice/notes.txt").ReadAllString(ctx)
```

Standard library only, so one module covers every WebDAV server. Paths map
to URL paths below the base URL, `PROPFIND` backs `Stat` and `ListDir`,
`MOVE` and `COPY` are native, and readers seek with `Range` requests.

### smbfs

```go
import "github.com/ungerik/go-fs/smbfs"

smbFS, err := smbfs.DialAndRegister(ctx, "smb://alice@nas.local/documents",
    &smbfs.Options{Password: "secret", Domain: "WORKGROUP"})
defer smbFS.Close()

files, err := fs.File("smb://alice@nas.local/documents/").ListDirMax(ctx, -1, "*.pdf")
```

SMB2/3 for Windows shares, Samba and NAS devices with the pure Go
[go-smb2](https://github.com/hirochachacha/go-smb2). Real directories and
random access file handles, so nearly every optional interface is native:
append and read-write handles, `Truncate`, `Touch`, `MakeAllDirs`,
`RemoveAll`, server-side `Move`, symbolic links. Permissions are the SMB
read-only attribute, so only the user write bit is stored.

### azureblobfs

```go
import "github.com/ungerik/go-fs/azureblobfs"

blobFS, err := azureblobfs.NewFromConnectionString(ctx, os.Getenv("AZURE_STORAGE_CONNECTION_STRING"), "assets", false)
defer blobFS.Close()

err = blobFS.RootDir().Join("images", "logo.png").WriteAll(ctx, logo)
```

Azure Blob Storage with the Azure SDK for Go; `azureblobfs.NewAndRegister`
takes a configured `container.Client` for other credential types.
Directories are blob name prefixes with marker blobs like in s3fs, reads
seek with range requests, `CopyFile` is a server-side copy and `Touch`
updates the modification time by setting the blob metadata.

### zipfs

```go
import "github.com/ungerik/go-fs/zipfs"

// Read a ZIP archive as a file system
zipFS, err := zipfs.NewReader(fs.File("archive.zip"))
defer zipFS.Close()

err = zipFS.RootDir().ListDir(ctx, func(f fs.File) error {
    fmt.Println(f.Path())
    return nil
})

// Write a new ZIP archive
out, err := zipfs.NewWriter(fs.File("out.zip"))
defer out.Close()
```

`Reader` is a `StdFileSystem` over the `io/fs.FS` of `archive/zip.Reader`;
`Writer` writes entries sequentially.

### tarfs

The same for tar archives, optionally gzip compressed (`.tar.gz`, `.tgz`):

```go
import "github.com/ungerik/go-fs/tarfs"

tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"))
defer tarFS.Close()

out, err := tarfs.NewWriter(fs.File("out.tgz"))
err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "...")
err = out.Close() // finishes the archive
```

A reader indexes the archive once and reads file content on demand; a
gzip compressed archive is decompressed into memory. A writer buffers each
file until its writer is closed, because tar needs the size before the
content.

### multipartfs

See the introduction for `multipartfs.FromRequestForm` — it wraps an
uploaded HTML form so files can be consumed using the regular `fs.File`
API. `multipartfs.New` does the same for a `*multipart.Form` that was
parsed by the caller.

The form fields with uploaded files are the directories of the file system,
so it has exactly two levels. Because a path has to identify exactly one
file, files uploaded under an already used name get a unique name
(`a.txt`, `a (2).txt`, ...), and names that are not usable as a path element
(`.`, `..`) become `unnamed`. Uploaded files carry no modification time.
`Close` removes the temporary files of the form; every method returns
`fs.ErrFileSystemClosed` afterwards.

### MemFileSystem

A fully-featured, thread-safe, in-memory file system useful for tests
or as a cache for slower backends:

```go
memFS, err := fs.NewMemFileSystem("/", fs.NewMemFile("hello.txt", []byte("hi")))
defer memFS.Close()

// Access through the global Registry using the URI prefix
data, err := fs.File(memFS.Prefix() + "/hello.txt").ReadAll(ctx)

// Or create a one-shot single-file FS that gives you a ready-to-use File
ms, file, err := fs.NewSingleMemFileSystem(fs.NewMemFile("a.txt", []byte("a")))
defer ms.Close()
```

`MemFileSystem` implements nearly every optional `FileSystem`
interface — including `RenameFileSystem`, `MoveFileSystem`,
`WatchFileSystem`, `PermissionsFileSystem`, `UserFileSystem`,
`GroupFileSystem`, `ListDirMaxFileSystem`, `ListDirRecursiveFileSystem`,
`XAttrFileSystem`, and `SymbolicLinkFileSystem` — so it can stand in
for any other backend in tests. Watch events are synthesized from
every mutation that goes through the FS API; direct mutation of a
`MemFile.FileData` byte slice obtained outside the API is not observable.

Standard library file systems and sub views
--------------------------------------------

`StdFileSystem` is the counterpart of `StdFS`: it adapts any `io/fs.FS`
as a read-only go-fs file system, so embedded assets, an `os.DirFS`
sandbox, a `zip.Reader` or a `testing/fstest.MapFS` work with the `File`
API:

```go
//go:embed templates/*
var templates embed.FS

tmplFS := fs.NewStdFileSystemAndRegister(templates, "templates")
defer tmplFS.Close()

html, err := fs.File("stdfs://templates/templates/index.html").ReadAllString(ctx)
```

`SubFileSystem` is a view of a directory of another file system as a file
system of its own. Every operation is forwarded to the parent with
translated paths, so the parent's native implementations are used and
paths can't escape the directory:

```go
subFS, err := fs.NewSubFileSystemAndRegister(fs.Local, "/srv/data", "data")
defer subFS.Close()

files, err := fs.File("sub://data/").ListDirMax(ctx, -1)
err = fs.File("sub://data/report.txt").WriteAllString(ctx, "...") // writes /srv/data/report.txt
```

`OverlayFileSystem` stacks a writable upper layer on a read-only base:
reads fall through to the base, listings are the union, every write goes
to the upper layer, base files modified in place are copied up first, and
removed base entries are hidden by in-memory whiteouts. Typical uses are
a scratch copy of embedded defaults, or tests that must not modify a
fixture:

```go
defaults := fs.NewStdFileSystemAndRegister(embeddedConfig, "defaults")
scratch, err := fs.NewMemFileSystem("/")
overlay, err := fs.NewOverlayFileSystemAndRegister(defaults, scratch, "config")
defer overlay.Close()

err = fs.File("overlay://config/app.json").WriteAllString(ctx, "...") // lands in scratch
data, err := fs.File("overlay://config/schema.json").ReadAll(ctx)   // read from defaults
```

Implementing a file system
--------------------------

A backend implements `FileSystem` (metadata, paths, `Stat`, `ListDir`,
`OpenReader`, `Close`) and, if it can write, `WriteFileSystem` (`OpenWriter`,
`MakeDir`, `Remove`). Everything else is an optional interface with a
generic emulation in this package, to be implemented only when the backend
can do it more efficiently. `fsimpl.PathHelper` provides the path methods
for a URI prefix, `fsimpl.NewWriteOnCloseFileBuffer` a writer for backends
without random access, `fsimpl.RangeReader` a seekable reader for backends
that read with byte range requests (`webdavfs`, `azureblobfs`),
`fsimpl.NewDirTree` a directory index for archives that have none (`tarfs`),
and `fstest.RunConformance` verifies a backend against the contract of every
method:

```go
func TestMyFS(t *testing.T) {
	fstest.RunConformance(t, myFS, fstest.Config{
		Name:    "My file system",
		Prefix:  "myfs://",
		TestDir: "/conformance", // an empty directory the suite may use
	})
}
```
