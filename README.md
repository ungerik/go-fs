go-fs: A unified file system for Go
===================================

[![Go Reference](https://pkg.go.dev/badge/github.com/ungerik/go-fs.svg)](https://pkg.go.dev/github.com/ungerik/go-fs)
[![Go Report Card](https://goreportcard.com/badge/github.com/ungerik/go-fs)](https://goreportcard.com/report/github.com/ungerik/go-fs)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ungerik/go-fs)](https://github.com/ungerik/go-fs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub release](https://img.shields.io/github/release/ungerik/go-fs.svg)](https://github.com/ungerik/go-fs/releases)

The package is built around a `File` type that is a string underneath
and interprets its value as a local file system path or as a URI.

Version 1.0 is a release candidate (v1.0.0-rc.1); the v1 API freezes with
the v1.0.0 release. Upgrading from v0.x is mechanical, see
[docs/MIGRATION_v1.md](docs/MIGRATION_v1.md).

Documentation
-------------

The rest of this README is an API tour. For task-oriented guides,
design rationale and the implementer reference see **[docs/](docs/README.md)**.

*Tutorials*

- [Getting started](docs/tutorial-getting-started.md) — read and write files
  against local disk, memory and HTTP with the same code
- [Implement a file system](docs/tutorial-implement-a-filesystem.md) — a
  complete backend in about 170 lines that passes the conformance suite

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

- [FileSystem interfaces](docs/reference-filesystem-interfaces.md) — all 27
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

`FileSystem` implementations are registered with their
URI prefixes like `file://` or `http://`.

The methods of `File` parse their string value for a prefix
and look up the `FileSystem` in the registry.
The only special rule is, that if no prefix is present,
then the string value is interpreted as a local file path.

The `LocalFileSystem` is registered by default.

Work with `Local` directly:

```go
fs.Local.Separator()         // Either `/` or `\`
fs.Local.CleanPath("~/file") // A leading "~/" expands to the home directory
fs.Local.CleanPath(".", "~") // "./~", only the first part expands the tilde
```

For example, create a `FileSystem` from a multi-part
HTTP form request that contains an uploaded file:

```go
import "github.com/ungerik/go-fs/multipartfs"

multipartFS, err := multipartfs.NewFromRequest(request, maxUploadMemory, "") // "" = random id
if err != nil {
    return err
}
defer multipartFS.Close() // unregisters it and removes the temporary files

// Register it, so that the fs.File values of the uploads resolve
err = fs.Register(multipartFS)

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

Its `fmt.Stringer` method `String` returns `URI()`: the prefix of the file
system followed by the clean path, like `file:///tmp/file.txt` for a local file.

Path related methods:

```go
file := fs.TempDir().Join("file.txt")

dir := file.Dir()   // "/tmp" == fs.TempDir()
name := file.Name() // "file.txt"
path := file.Path() // "/tmp/file.txt"
uri := file.URI()   // "file:///tmp/file.txt"
ext := file.Ext()   // ".txt"
lower := file.ExtLower() // ".txt"
trimmed := file.TrimExt() // "/tmp/file"

slashed := file.PathWithSlashes() // forward slashes regardless of OS
local := file.LocalPath()         // "" if not on Local
mustLocal := file.MustLocalPath() // panics if not on Local

file2 := file.Dir().Join("a", "b", "c").Joinf("file%d.txt", 2)
path2 := file2.Path() // "/tmp/a/b/c/file2.txt"

abs := fs.File("~/some-dir/../file").AbsPath()  // "/home/erik/file"
isAbs := fs.File("relative/path").HasAbsPath()  // false
absFile := fs.File("relative/path").ToAbsPath() // File with an absolute path

// Relative path between two files on the same FileSystem
base := fs.File("/tmp/project")
rel, err := base.RelPathOf(base.Join("a", "b", "c")) // "a/b/c"
```

Temporary files:

```go
tmpFile := fs.TempFile(".json")             // random name in the temp dir, not created yet
created, err := fs.CreateTempFile(".json")  // created atomically, like os.CreateTemp
tmpDir, err := fs.MakeTempDir()             // a new directory in the temp dir
```

Access and existence checks:

```go
file.IsReadable() // file exists and is readable
file.IsWritable() // file (or its parent dir) is writable
file.IsEmptyDir() // directory exists and contains no entries
```

Ownership (where the file system supports it, e.g. `LocalFileSystem` on Unix):

```go
user, err := file.User()
err = file.SetUser("alice")

group, err := file.Group()
err = file.SetGroup("staff")
```

Resizing existing files (native where supported, emulated elsewhere):

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
info := file.Info().StdFileInfo() // io/fs.FileInfo
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
r, err := file.OpenReadSeeker() // fs.ReadAtSeekCloser
```

Writing:

```go
err := file.WriteAll(ctx, []byte("Hello"))

err = file.WriteAllString(ctx, "Hello")

err = file.Append(ctx, []byte("Hello"))

err = file.AppendString(ctx, "Hello")

var r io.Reader
n, err := file.ReadFrom(r)

w, err := file.OpenWriter()      // io.WriteCloser
w, err = file.OpenAppendWriter() // io.WriteCloser

rw, err := file.OpenReadWriter() // fs.ReadWriteSeekCloser
```

Writers that commit the data only in `Close`, like the uploads of the object
store backends, implement `fs.WriteAborter`. `Abort` discards the written data
instead of committing it, and `fs.CopyFile`, `File.ReadFrom` and the package's
own copies call it when a copy fails or the context is canceled, so no partial
upload replaces a file.

Copying, moving and renaming
----------------------------

```go
err := fs.CopyFile(ctx, src, dest)         // src is any FileReader, dest a File
err = fs.Move(ctx, source, destination)    // files and directories
err = source.MoveTo(ctx, destination)      // the same as a method
renamed, err := file.Rename(ctx, "new-name.txt") // within the directory
```

All of them work across file systems. Within one file system the native copy
or move of the backend is used if it has one, else the data is copied, and a
move removes the source only after the copy succeeded.

The destination rules are the same on every file system:

| Destination        | Move, MoveTo and Rename                                      | CopyFile                |
| ------------------ | ------------------------------------------------------------ | ----------------------- |
| missing            | proceeds                                                     | proceeds                |
| existing file      | replaced, like by `os.Rename` (`ErrIsNotDirectory` for a directory source) | replaced  |
| existing directory | an error wrapping `os.ErrExist`, never merged or deleted     | `ErrIsDirectory`        |

`fs.Move`, `File.MoveTo` and `fs.CopyFile` first resolve a destination that is
an existing directory to the source's name inside it, so the last row only
applies to `Rename` and to a directory that is already there under the
source's name. A destination equal to the source is a no-op, and moving a
directory into itself or one of its sub-directories is an error wrapping
`os.ErrInvalid`. `Rename` rejects a name that is empty, `.` or `..`, or that
contains a slash or the separator of the file system, with an error wrapping
`os.ErrInvalid`. [Copy, move and compare](docs/howto-copy-move-and-compare.md)
has the details.

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
memFile, err = fs.ReadAllMemFile(ctx, r, "in-mem-file.txt")
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
files, err = dir.ListDirMax(ctx, 100, "*.jpg", "*.jpeg")

// Recursive variant with a hard cap
files, err = dir.ListDirRecursiveMax(ctx, 1000, "*.go")
```

Iterator methods (`iter.Seq2[fs.File, error]`):

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

Glob yields the files that match a path pattern, also as
`iter.Seq2[fs.File, error]`:

```go
// All Go files in the sub-directories of "cmd"
for file, err := range fs.Glob(ctx, "cmd/*/*.go") {
	if err != nil {
		return err
	}
	fmt.Println(file.Path())
}

// Relative to a specific base directory
for file, err := range dir.Glob(ctx, "*/*.png") {
	if err != nil {
		return err
	}
	fmt.Println(file.Path())
}
```

The pattern syntax is that of `path.Match`, with `/` as separator on every
file system; every pattern segment matches one path element, `..` segments
are not allowed. A pattern ending with a slash (`/`) only matches
directories. A malformed pattern, an error listing a directory and the
error of a canceled context are yielded together with `fs.InvalidFile`; the
iteration stops after a pattern or context error and continues after a
listing error if the loop does.

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
err = fs.File("config.json").WriteJSON(ctx, &cfg)
err = fs.File("config.json").WriteJSONIndent(ctx, &cfg, "  ") // indented

err = fs.File("config.xml").ReadXML(ctx, &cfg)
err = fs.File("config.xml").WriteXMLIndent(ctx, &cfg, "  ")
```

The same helpers are also available as `MemFile` constructors:

```go
m, err := fs.NewMemFileWriteJSONIndent("config.json", &cfg, "  ")
m, err = fs.NewMemFileWriteXML("config.xml", &cfg)
```

Symbolic links
--------------

File systems opt into symbolic link support by implementing the
`SymbolicLinkFileSystem` interface. `LocalFileSystem`, `MemFileSystem`,
`SubFileSystem` (forwarding to its parent), `sftpfs` and `smbfs` opt in.
On the other backends `CreateSymbolicLink` and `ReadSymbolicLink` return an
`ErrUnsupported` error, `IsSymbolicLink` is false, and `Lstat` returns the
same as `Stat`.

```go
target := fs.File("/etc/hosts")
link := fs.File("/tmp/hosts-link")

// Create link as a symbolic link pointing to target
err := link.CreateSymbolicLink(target)

// Lstat describes the link itself, Stat and Info follow it to the target
info, err := link.Lstat() // info.IsSymlink == true
isLink := link.IsSymbolicLink() // true

// Read the link target as stored on disk (may be relative)
resolved, err := link.ReadSymbolicLink()
```

Both files must live on the same `FileSystem`; a target from a different
file system is an error wrapping `fs.ErrFileSystemsDoNotMatch`. The target
path is stored verbatim, so pass an absolute path if you want the link to
resolve the same way regardless of where it is read from.

The `FileInfo` of a directory listing describes the entry itself, like
`Lstat`: for a link `IsSymlink` is true and `IsDir` and `IsRegular` describe
the target as far as the file system knows it cheaply.

Recursive operations never descend into a link to a directory, like
`filepath.WalkDir`, so a link cycle or a link to `/` can't make them walk
the same files again or the whole disk:

- `ListDirRecursive` lists a link to a directory like a file, with
  `IsSymlink` true and `IsDir` false, and doesn't list the files of the
  directory it points to. Only a link you start the listing at is followed.
- `fs.CopyRecursive` copies a link to a file as a file with the target's
  content. Within one file system that implements `SymbolicLinkFileSystem` it
  copies a link to a directory as a link with the target kept as it is.
  Another file system can resolve the same target to another directory, even
  a relative target inside the copied directory, so between file systems a
  link to a directory is never copied: the copy fails before it writes
  anything, with an error wrapping `errors.ErrUnsupported` that names the link.
- `fs.CopyRecursive` never writes through a symbolic link below the
  destination: a file or directory over a link there is an error wrapping
  `os.ErrExist`. The destination directory itself may be a link. The links
  are checked by path before each write, so this assumes that nobody else
  changes the destination during the copy.
- `fs.Move` between file systems copies links the same way and only removes
  the source after the copy, so a link to a directory, the moved source itself
  or one in a moved directory, can't be moved to another file system. Within
  one file system that supports symbolic links, a move keeps every link
  a link, like `os.Rename`.
  Moving a link onto the file it points to is an error wrapping
  `os.ErrInvalid`, like `mv` reports "are the same file", on the local and the
  in-memory file system, with the native `Move` of `sftpfs` and `smbfs`, and
  with the copy-then-delete move of file systems without a native `Move`.

Extended attributes
-------------------

`LocalFileSystem` and `MemFileSystem` implement the `XAttrFileSystem`
interface; other file systems return an `ErrUnsupported` error.

```go
err := file.SetXAttr("user.origin", []byte("upload"))                 // create or replace
err = file.SetXAttr("user.origin", []byte("upload"), fs.XAttrCreate)  // fails if it exists
err = file.SetXAttr("user.origin", []byte("import"), fs.XAttrReplace) // fails if it doesn't

value, err := file.GetXAttr("user.origin")
names, err := file.ListXAttr()
err = file.RemoveXAttr("user.origin")
```

These methods follow symbolic links; `LSetXAttr`, `LGetXAttr`, `LListXAttr`
and `LRemoveXAttr` act on the link itself. On Linux the names need a namespace
like `user.`. The error of `XAttrCreate` wraps `os.ErrExist`, the errors of
`XAttrReplace` and of getting or removing a missing attribute wrap
`os.ErrNotExist`.

Watching files
--------------

The local file system supports watching files and directories for changes using the `fsnotify` package.
This functionality is available through the `WatchFileSystem` interface and the `File.Watch` method.
`MemFileSystem` implements it too, and `SubFileSystem` forwards it to its parent;
on other file systems `Watch` returns an `ErrUnsupported` error.

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

`fs.Event` is a bitmask of the constants `fs.EventCreate`, `fs.EventWrite`,
`fs.EventRemove`, `fs.EventRename` and `fs.EventChmod`, because one event can
report several operations. `String` joins their names, like `CREATE|WRITE`.
- `HasCreate()` - File or directory was created
- `HasWrite()` - File was written to
- `HasRemove()` - File or directory was removed
- `HasRename()` - File or directory was renamed
- `HasChmod()` - File permissions or attributes were changed

**Important Notes:**
- Watching a directory reports changes of the directory and of the entries directly within it, not in recursive sub-directories
- Multiple watches can be set on the same file/directory
- The returned `cancel` function stops a specific watch; queued events of that watch are dropped
- The callbacks of one watch are called serially, in the order of the events, on a goroutine of the file system, so a slow callback only delays the later events of its own watch

**Logging:**
You can enable logging for watch events and errors.
Set the loggers before calling `Watch`: a change applies with the next `Watch` call.

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
`zipfs`, `tarfs`, `multipartfs`) are packages in the root module.

`File` values route to the file system that is registered for their URI
prefix. The constructors follow one rule:

- `New…` and `Dial…` return the file system, and an error where construction
  can fail, and never register it.
- `…AndRegister` registers the file system and returns only an error. It
  exists where the prefix follows from the arguments, so the caller can use
  the URIs without the instance.
- A temporary file system is created with `New…`, registered with
  `fs.Register` and closed with `defer Close()`, which also unregisters it.

Only `httpfs` registers file systems when it is imported.

| Package       | URI prefix              | Constructors                                       | Read | Write  |
| ------------- | ----------------------- | -------------------------------------------------- | :--: | :----: |
| (built-in)    | `file://` or none       | `fs.Local` (registered by default)                 | yes  | yes    |
| `httpfs`      | `http://`, `https://`   | side-effect import: `import _ ".../httpfs"`; `httpfs.New`, `httpfs.NewAndRegister` for a prefix with its own client | yes  | no     |
| `s3fs`        | `s3://<bucket>`         | `s3fs.New`, `s3fs.NewLoadDefaultConfig`, `s3fs.NewAndRegister` | yes  | yes/ro |
| `sftpfs`      | `sftp://<user>@<host>`  | `sftpfs.Dial`, `DialAndRegister`, `EnsureRegistered` | yes | yes  |
| `ftpfs`       | `ftp://[<user>@]<host>`, `ftps://[<user>@]<host>` | `ftpfs.Dial`, `DialAndRegister`, `EnsureRegistered` | yes | yes   |
| `dropboxfs`   | `dropbox://<account id without "dbid:">` | `dropboxfs.New`, `dropboxfs.NewAndRegister`       | yes  | yes    |
| `webdavfs`    | `webdav://[<user>@]<host><base path>` | `webdavfs.New`, `webdavfs.NewAndRegister` | yes  | yes    |
| `smbfs`       | `smb://[<user>@]<host>/<share>` | `smbfs.Dial`, `DialAndRegister`, `EnsureRegistered` | yes  | yes    |
| `azureblobfs` | `azblob://<host>/<container>` | `azureblobfs.New`, `NewFromConnectionString`, `NewAndRegister` | yes | yes/ro |
| `zipfs`       | `zip://<id>`            | `zipfs.NewReader`, `zipfs.NewWriter`               | reader | writer |
| `tarfs`       | `tar://<id>`            | `tarfs.NewReader`, `tarfs.NewWriter`               | reader | writer |
| `multipartfs` | `multipart://<id>`      | `multipartfs.NewFromRequest`, `multipartfs.New`    | yes  | no     |
| (built-in)    | `mem://<id>`            | `fs.NewMemFileSystem`, `fs.NewMemFileSystemAndRegister` | yes  | yes    |
| (built-in)    | `stdfs://<id>`          | `fs.NewStdFileSystem(fsys, id)`, `fs.NewStdFileSystemAndRegister`: embed.FS, os.DirFS, zip.Reader, MapFS | yes | no |
| (built-in)    | `sub://<id>`            | `fs.NewSubFileSystem(parent, dir, id)`, `fs.NewSubFileSystemAndRegister`: view rooted at a directory | as parent | as parent |
| (built-in)    | `overlay://<id>`        | `fs.NewOverlayFileSystem(base, upper, id)`, `fs.NewOverlayFileSystemAndRegister`: writable layer over a read-only base | yes | yes |

The prefixes of `sftpfs`, `ftpfs` and `smbfs` add `:<port>` for a port other
than the default of the protocol, and a `MemFileSystem` with a volume has the
prefix `mem://<id>/`, so the URI of `C:\dir` is `mem://<id>/C:\dir`. An empty
id of the archive, form, memory, standard library, sub and overlay file
systems means a random one; the `…AndRegister` variants need an id.

A URI matches a registered prefix only at a boundary: the URI ends after the
prefix, the prefix ends with a slash, or the prefix is followed by a slash or
the separator of the file system. So the prefix `s3://logs` matches
`s3://logs/key` but not `s3://logs-archive/key`. The longest matching prefix
or prefix alias wins.

`fs.Register` returns an error wrapping `fs.ErrAlreadyRegistered` if another
file system holds the prefix; registering the same instance again is a no-op.
`fs.Unregister` removes exactly the passed instance. `fs.EnsureRegistered`
returns the file system registered for a prefix or creates it, shares one
creation between concurrent callers, and returns a `free` function that
closes a file system it created when the last caller freed it. The
`EnsureRegistered` functions of `sftpfs`, `ftpfs` and `smbfs` are built on it.

Every registered file system has an `ID()`: a string that identifies it
uniquely among the registered file systems and stays the same for its whole
lifetime, computed at construction and never blocking.

Where the backing store has an identifier of its own, that one is used. Only
two do: the local file system has the volume's file system id, and Dropbox
has the account id. Neither SFTP, FTP, WebDAV, SMB, S3 nor Azure Blob Storage
expose one, so those use the coordinates that identify the store instead.

| File system                                        | `ID()`                                             |
| -------------------------------------------------- | -------------------------------------------------- |
| `fs.Local`                                         | File system id of the root volume: the `statfs` `f_fsid` on Unix (`statvfs` on NetBSD, Solaris and illumos), the volume serial number on Windows, `local` on js and wasip1 |
| `MemFileSystem`                                    | `MemFileSystemOptions.ID`, random if empty         |
| `StdFileSystem`, `SubFileSystem`, `OverlayFileSystem` | The id part of the prefix, random if none was passed |
| `httpfs`                                           | The prefix without a trailing `://`: `http` / `https` for `httpfs.Default` / `httpfs.DefaultTLS` |
| `zipfs`, `tarfs`, `multipartfs`                    | The prefix of the archive or form: `zip://<id>`, `tar://<id>`, `multipart://<id>` |
| `s3fs`                                             | Bucket name, or `Options.Prefix` if it was set     |
| `azureblobfs`                                      | `<host>/<container>`                               |
| `sftpfs`, `ftpfs`                                  | The connection prefix, `sftp://user@host` / `ftp://[user@]host` |
| `smbfs`                                            | `[<user>@]<host>/<share>`                          |
| `webdavfs`                                         | `[<user>@]<host><base path>`                       |
| `dropboxfs`                                        | Dropbox account id with its `dbid:` part, which the prefix omits |

The format is specific to the implementation and promises nothing beyond
uniqueness and stability. Use it to tell two file systems apart, to key a
cache, or as meta information — don't parse it. `Prefix()` builds URIs,
`Name()` is for human readable output.

### Optional interface support

Every backend implements the core `FileSystem` interface. Beyond that, the
package defines small *optional* interfaces (`CopyFileSystem`, `MoveFileSystem`,
`TouchFileSystem`, …). When a backend does not implement one, the operation
still works through a generic emulation built on the core methods — a backend
only implements an optional interface when doing so is more efficient or more
capable than that emulation.

`LocalFileSystem` and `MemFileSystem` implement almost every optional
interface natively. `LocalFileSystem` leaves `Exists` (a `Stat`) and
`ListDirRecursive` (no faster than the generic walk) to the emulation and
implements `User`/`Group` only on Unix. `MemFileSystem` implements `Exists`,
`ListDirRecursive` and `User`/`Group` too, and leaves out `IsHidden` and the
absolute path methods, whose defaults fit it. For the remote backends:

| Capability             | s3  | azblob | sftp | ftp | smb | dropbox | webdav | http |
| ---------------------- | :-: | :----: | :--: | :-: | :-: | :-----: | :----: | :--: |
| CopyFile (server-side) | ✓   | ✓      | –    | –   | –   | ✓       | ✓      | –    |
| Move                   | –   | ✓      | ✓    | ✓   | ✓   | ✓       | ✓      | –    |
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
`zipfs.Reader` and `tarfs.Reader` provide `Exists`, `ReadAll` and
`ListDirRecursive`, the writers provide `Touch` and `MakeAllDirs`
(`tarfs.Writer` also `WriteAll`), and `multipartfs` is read-only and provides
`Exists` and `ReadAll`. `httpfs` implements `URIFileSystem`, because its paths
are URLs in their escaped form.

Every backend follows the same error contract, checked by the conformance
suite in `fstest`: `errors.Is(err, os.ErrNotExist)` for missing files,
`os.ErrExist` for `MakeDir` on an existing path,
`errors.Is(err, fs.ErrIsDirectory{})`, `fs.ErrIsNotDirectory{}` and
`fs.ErrDirectoryNotEmpty{}` for the directory cases,
`fs.ErrReadOnlyFileSystem` / `fs.ErrWriteOnlyFileSystem` for the wrong
direction, `fs.ErrFileSystemClosed` after `Close`, and the context error when
a context is cancelled. A URI with a scheme that no file system is registered
for resolves to the invalid file system, whose operations return an error
wrapping `fs.ErrInvalidFileSystem` that names the URI with its password
redacted. An empty path is `fs.ErrEmptyPath`.
[docs/reference-errors.md](docs/reference-errors.md) has the full list.

Methods only take a `context.Context` when they can take long: reading or
writing content, listing directories, removing recursively, copying, moving
and renaming, truncating, and dialing connections.

### httpfs

```go
import _ "github.com/ungerik/go-fs/httpfs"

data, err := fs.File("https://example.com/file.txt").ReadAll(ctx)
```

Read-only. Useful for treating remote files uniformly with local ones.
Importing the package registers `httpfs.Default` and `httpfs.DefaultTLS`,
which use `http.DefaultClient`. To use another `*http.Client`, register a file
system for a more specific prefix; URLs with that prefix then use it:

```go
err := httpfs.NewAndRegister("https://api.example.com/", &httpfs.Options{
    Client: &http.Client{Timeout: 30 * time.Second},
})
```

URLs are used verbatim: the query and escapes in the path are passed to the
server unchanged. [httpfs/README.md](httpfs/README.md) has the details.

### s3fs

```go
import "github.com/ungerik/go-fs/s3fs"

// Using the default AWS credential chain
bucket, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", nil)
if err != nil {
    return err
}
defer bucket.Close()
err = fs.Register(bucket)

// Or registered for the lifetime of the program with an existing aws-sdk-go-v2 client
err = s3fs.NewAndRegister(client, "other-bucket", &s3fs.Options{ReadOnly: true})

err = fs.File("s3://my-bucket/path/file.txt").WriteAllString(ctx, "Hello")
```

Uploads from `Options.MultipartUploadThreshold` (16 MiB) on are multipart
uploads with parts of `Options.PartSize` (8 MiB), `ReadAll` downloads objects
from `Options.MultipartDownloadThreshold` (16 MiB) on with concurrent range
requests, and writers from `OpenWriter` upload the parts while they are
written. Directories are object key prefixes; `MakeDir` creates a zero-byte
marker object so empty directories exist too.
[s3fs/README.md](s3fs/README.md) has the credential setup, the
S3-compatible service configuration and the full concept mapping.

### sftpfs

```go
import (
    "github.com/ungerik/go-fs/sftpfs"
    "golang.org/x/crypto/ssh/knownhosts"
)

// knownhosts.New does not expand "~"
home, err := os.UserHomeDir()
if err != nil {
    return err
}
hostKeys, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
if err != nil {
    return err
}

err = sftpfs.DialAndRegister(ctx, "sftp://user@host", &sftpfs.Options{
    Password:        "secret",
    HostKeyCallback: hostKeys, // required; sftpfs.AcceptAnyHostKey only for tests
})
if err != nil {
    return err
}

data, err := fs.File("sftp://user@host/etc/hostname").ReadAll(ctx)
```

A lost connection is reconnected; operations that can't be repeated are only
retried if the connection was lost before their request. `EnsureRegistered`
shares one connection per address between callers with reference counting.
Pass URIs with credentials, like `sftp://user:password@host`, to
`EnsureRegistered`; the password is not part of the prefix.
[sftpfs/README.md](sftpfs/README.md) has the host key setup and the full
concept mapping.

### ftpfs

```go
import "github.com/ungerik/go-fs/ftpfs"

err := ftpfs.DialAndRegister(ctx, "ftps://example.com", &ftpfs.Options{
    Username: "user",
    Password: "secret",
    // TLSConfig, InsecureSkipVerify, DebugOut for the protocol log
})
if err != nil {
    return err
}

data, err := fs.File("ftps://user@example.com/pub/notes.txt").ReadAll(ctx)
```

`ftp://` is plain FTP, `ftps://` is explicit TLS on port 21 (implicit TLS
with port 990). Server certificates are verified unless
`ftpfs.Options.InsecureSkipVerify` is set. The single control connection is
used by one operation at a time; `OpenReader` streams over its own connection.
[ftpfs/README.md](ftpfs/README.md) has the TLS options and the full concept
mapping.

### dropboxfs

```go
import "github.com/ungerik/go-fs/dropboxfs"

dbxFS, err := dropboxfs.New(ctx, accessToken, &dropboxfs.Options{CacheTimeout: 5 * time.Minute})
if err != nil {
    return err
}
defer dbxFS.Close()
err = fs.Register(dbxFS)

// The prefix is dropbox://<account id without "dbid:">,
// so it is stable per account
err = dbxFS.RootDir().Join("Apps", "MyApp", "notes.md").WriteAllString(ctx, "...")
```

`Options.CacheTimeout` is the metadata cache timeout (zero disables the
cache), `Options.Mute` mutes the notifications Dropbox sends for changed
files, and `Options.HTTPClient` takes a client that refreshes short-lived
access tokens. The prefix depends on the account, so `New` and `fs.Register`
are the usual pair; `NewAndRegister` is for callers that know the account id.
[dropboxfs/README.md](dropboxfs/README.md) has the app and access token
setup and how to run the tests against a real account.

### webdavfs

```go
import "github.com/ungerik/go-fs/webdavfs"

davFS, err := webdavfs.New(ctx, "https://cloud.example.com/remote.php/dav/files/alice",
    &webdavfs.Options{Username: "alice", Password: "app-password"})
if err != nil {
    return err
}
defer davFS.Close()
err = fs.Register(davFS)

notes, err := fs.File("webdav://alice@cloud.example.com/remote.php/dav/files/alice/notes.txt").ReadAllString(ctx)
```

Standard library only, so one module covers every WebDAV server. Paths map
to URL paths below the base URL, and the username is part of the prefix,
because some servers show every user their own files at the same URL.
`PROPFIND` backs `Stat` and `ListDir`, `MOVE` and `COPY` are native, and
readers seek with `Range` requests.
[webdavfs/README.md](webdavfs/README.md) has the authentication options and
the full concept mapping.

### smbfs

```go
import "github.com/ungerik/go-fs/smbfs"

smbFS, err := smbfs.Dial(ctx, "smb://nas.local/documents", &smbfs.Options{
    Username: "alice",
    Password: "secret",
    Domain:   "WORKGROUP",
})
if err != nil {
    return err
}
defer smbFS.Close()
err = fs.Register(smbFS)

files, err := fs.File("smb://alice@nas.local/documents/").ListDirMax(ctx, -1, "*.pdf")
```

SMB2/3 for Windows shares, Samba and NAS devices with the pure Go
[go-smb2](https://github.com/cloudsoda/go-smb2). Real directories and
random access file handles, so nearly every optional interface is native:
append and read-write handles, `Truncate`, `Touch`, `MakeAllDirs`,
`RemoveAll`, server-side `Move`, symbolic links. Permissions are the SMB
read-only attribute, so only the user write bit is stored.
[smbfs/README.md](smbfs/README.md) has the authentication options and the
full concept mapping.

### azureblobfs

```go
import "github.com/ungerik/go-fs/azureblobfs"

blobFS, err := azureblobfs.NewFromConnectionString(ctx, os.Getenv("AZURE_STORAGE_CONNECTION_STRING"), "assets", nil)
if err != nil {
    return err
}
defer blobFS.Close()
err = fs.Register(blobFS)

err = blobFS.RootDir().Join("images", "logo.png").WriteAll(ctx, logo)
```

Azure Blob Storage with the Azure SDK for Go; `azureblobfs.New` and
`azureblobfs.NewAndRegister` take a configured `container.Client` for other
credential types. Directories are blob name prefixes with marker blobs like
in s3fs, reads seek with range requests, writers from `OpenWriter` upload
blocks while they are written, `CopyFile` is a server-side copy and `Touch`
updates the modification time by setting the blob metadata.
[azureblobfs/README.md](azureblobfs/README.md) has the credential setup and
the full concept mapping.

### zipfs

```go
import "github.com/ungerik/go-fs/zipfs"

// Read a ZIP archive as a file system
zipFS, err := zipfs.NewReader(fs.File("archive.zip"), "") // "" = random id
if err != nil {
    return err
}
defer zipFS.Close()
err = fs.Register(zipFS)

err = zipFS.RootDir().ListDir(ctx, func(f fs.File) error {
    fmt.Println(f.Path())
    return nil
})

// Write a new ZIP archive
out, err := zipfs.NewWriter(fs.File("out.zip"), "")
if err != nil {
    return err
}
err = fs.Register(out)
err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "...")
err = out.Close() // writes the central directory
```

`Reader` reads the entries at their offsets in the archive, in place if the
file system of the archive has seekable readers, and decompresses them on
demand. `Writer` buffers each file in memory until its writer is closed;
several files can be written at the same time, and `Abort` of a writer
discards the file. [zipfs/README.md](zipfs/README.md) has the details.

### tarfs

The same for tar archives, optionally gzip compressed (`.tar.gz`, `.tgz`):

```go
import "github.com/ungerik/go-fs/tarfs"

tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"), "")
if err != nil {
    return err
}
defer tarFS.Close()
err = fs.Register(tarFS)

out, err := tarfs.NewWriter(fs.File("out.tgz"), "")
if err != nil {
    return err
}
err = fs.Register(out)
err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "...")
err = out.Close() // finishes the archive
```

A reader indexes the archive once and reads file content on demand; a
gzip compressed archive is decompressed into memory. A writer buffers each
file until its writer is closed, because tar needs the size before the
content. [tarfs/README.md](tarfs/README.md) has the details.

### multipartfs

See the introduction for `multipartfs.NewFromRequest` — it wraps an
uploaded HTML form so files can be consumed using the regular `fs.File`
API. `multipartfs.New` does the same for a `*multipart.Form` that was
parsed by the caller. Neither registers the file system.

The form fields with uploaded files are the directories of the file system, so
it has exactly two levels. Because a path has to identify exactly one file,
files uploaded under an already used name get a unique name (`a.txt`, `a
(2).txt`, ...), and names that are not usable as a path element (`.`, `..`)
become `unnamed`. Uploaded files carry no modification time. `Close` removes
the temporary files of the form; afterwards every method that returns an error
returns `fs.ErrFileSystemClosed`, `FormValue` and `FormValues` stay usable.
[multipartfs/README.md](multipartfs/README.md) has the details.

### MemFileSystem

A fully-featured, thread-safe, in-memory file system useful for tests
or as a cache for slower backends:

```go
memFS, err := fs.NewMemFileSystem(nil, fs.NewMemFile("hello.txt", []byte("hi")))
if err != nil {
    return err
}
defer memFS.Close()
err = fs.Register(memFS) // a random mem://<id> prefix, isolated from other instances

data, err := memFS.RootDir().Join("hello.txt").ReadAll(ctx)

// With a fixed id, registered for the lifetime of the program
err = fs.NewMemFileSystemAndRegister(&fs.MemFileSystemOptions{ID: "fixtures"},
    fs.NewMemFile("a.txt", []byte("a")),
)
data, err = fs.File("mem://fixtures/a.txt").ReadAll(ctx)
```

`MemFileSystemOptions` sets the `ID`, the `Separator` (`/` or `\`), an
optional `Volume` like `C:` and `ReadOnly`; nil options use a random id and
`/`. `MemFileSystem` implements nearly every optional `FileSystem` interface
— including `MoveFileSystem`, `CopyFileSystem`, `WatchFileSystem`,
`PermissionsFileSystem`, `UserFileSystem`, `GroupFileSystem`,
`ListDirRecursiveFileSystem`, `XAttrFileSystem`, and
`SymbolicLinkFileSystem` — so it can stand in for any other backend in tests.
The file system stores copies of the data it is given and `ReadAll` returns
a copy, so watch events are synthesized from every mutation that goes through
the FS API, and changing a byte slice outside of it changes nothing.

Standard library file systems and sub views
--------------------------------------------

`StdFileSystem` is the counterpart of `StdFS`: it adapts any `io/fs.FS`
as a read-only go-fs file system, so embedded assets, an `os.DirFS`
sandbox, a `zip.Reader` or a `testing/fstest.MapFS` work with the `File`
API:

```go
//go:embed templates/*
var templates embed.FS

err := fs.NewStdFileSystemAndRegister(templates, "templates")

html, err := fs.File("stdfs://templates/templates/index.html").ReadAllString(ctx)
```

`SubFileSystem` is a view of a directory of another file system as a file
system of its own. Every operation is forwarded to the parent with
translated paths, so the parent's native implementations are used and
paths can't escape the directory. Over `fs.Local` the names that
`filepath.Localize` rejects, like `NUL`, `COM1` or `a:b` on Windows,
are an error wrapping `os.ErrInvalid`, as with `os.DirFS`.
The parent must stay registered while the view is used:

```go
err := fs.NewSubFileSystemAndRegister(fs.Local, "/srv/data", "data")

files, err := fs.File("sub://data/").ListDirMax(ctx, -1)
err = fs.File("sub://data/report.txt").WriteAllString(ctx, "...") // writes /srv/data/report.txt
```

`OverlayFileSystem` stacks a writable upper layer on a read-only base:
reads fall through to the base, listings are the union, every write goes
to the upper layer, base files modified in place are copied up first, and
removed base entries are hidden by in-memory whiteouts. The whiteouts are
recorded for the exact path, so both layers must be case-sensitive, or every
path must always be used with the same case: over a case-insensitive base a
removed file stays readable through a path with another case. Typical uses are
a scratch copy of embedded defaults, or tests that must not modify a
fixture. Both layers must stay registered while the overlay is used:

```go
defaults := fs.NewStdFileSystem(embeddedConfig, "defaults")
err := fs.Register(defaults)

scratch, err := fs.NewMemFileSystem(nil)
if err != nil {
    return err
}
err = fs.Register(scratch)

err = fs.NewOverlayFileSystemAndRegister(defaults, scratch, "config")

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
without random access that implements `fs.WriteAborter`,
`fsimpl.RangeReader` a seekable reader for backends that read with byte
range requests (`webdavfs`, `azureblobfs`), `fsimpl.RedactURI`,
`fsimpl.RedactAddress` and `fsimpl.RedactAddressWithoutScheme` the password
redaction for error messages, and `fstest.RunConformance`
verifies a backend against the contract of every method:

```go
func TestMyFS(t *testing.T) {
	fstest.RunConformance(t, myFS, fstest.Config{
		Name:    "My file system",
		Prefix:  "myfs://",
		TestDir: "/conformance", // an empty directory the suite may use
	})
}
```
