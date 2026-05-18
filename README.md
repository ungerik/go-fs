go-fs: A unified file system for Go
===================================

[![Go Reference](https://pkg.go.dev/badge/github.com/ungerik/go-fs.svg)](https://pkg.go.dev/github.com/ungerik/go-fs)
[![Go Report Card](https://goreportcard.com/badge/github.com/ungerik/go-fs)](https://goreportcard.com/report/github.com/ungerik/go-fs)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ungerik/go-fs)](https://github.com/ungerik/go-fs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GitHub release](https://img.shields.io/github/release/ungerik/go-fs.svg)](https://github.com/ungerik/go-fs/releases)

The package is built around a `File` type that is a string underneath
and interprets its value as a local file system path or as a URI.

TODO
----

- [ ] Return ErrFileSystemClosed from all closable FS
- [ ] Test dropboxfs
- [ ] Consider secsy/goftp for ftpfs

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
multipartFS.Form.Value["email"] 

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
as debug information and `gob.GobEncoder`, `gob.GobDecoder` to
encode filename and content instead of the path/URI value.

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
err := file.Truncate(1024) // resize to exactly 1024 bytes
```

Meta information:

```go
size := file.Size() // int64, 0 for non existing or dirs
isDir := dir.IsDir()      // true
exists := file.Exists()   // true
fileIsDir := file.IsDir() // false
modTime := file.Modified()
hash, err := file.ContentHash()  // Dropbox hash algo
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

Note that `MemFile` is not a `File` because it doesn't have a path or URI.
The in-memory `FileName` is not interpreted as a path and should not contain
path separators.

Derive new `MemFile` values without copying the underlying data:

```go
renamed := memFile.WithName("renamed.txt")  // same FileData, different name
patched := memFile.WithData(newBytes)       // same FileName, different data
```

Listing directories
-------------------

Callback-based listing (cancel by returning an error from the callback or
canceling the context):

```go
// Print names of all entries in dir
dir.ListDir(func(f fs.File) error {
	_, err := fmt.Println(f.Name())
	return err
})

// Print names of all JPEGs in dir and all recursive sub-dirs
// with cancelable context
dir.ListDirRecursiveContext(ctx, func(f fs.File) error {
	_, err := fmt.Println(f.Name())
	return err
}, "*.jpg", "*.jpeg")

// Get all files in dir without limit
files, err := dir.ListDirMax(-1)

// Get the first 100 JPEGs in dir
files, err := dir.ListDirMaxContext(ctx, 100, "*.jpg", "*.jpeg")

// Recursive variant with a hard cap
files, err := dir.ListDirRecursiveMax(1000, "*.go")
```

Go 1.23+ iterator methods (`iter.Seq2[fs.File, error]`):

```go
// Range directly over directory entries
for file, err := range dir.ListDirIter("*.jpg", "*.jpeg") {
	if err != nil {
		return err
	}
	fmt.Println(file.Name())
}

// Recursive iteration with a cancelable context
for file, err := range dir.ListDirRecursiveIterContext(ctx, "*.go") {
	if err != nil {
		return err
	}
	fmt.Println(file.Path())
}
```

Channel-based listing for fan-out pipelines (the `cancel` channel stops
the goroutine if any value is sent into it):

```go
cancel := make(chan error)
files, errs := dir.ListDirChan(cancel, "*.log")

for f := range files {
	process(f)
}
if err := <-errs; err != nil {
	return err
}

// Recursive variant
files, errs = dir.ListDirRecursiveChan(cancel, "*.log")
```

Glob with wildcard substitution (Go 1.23+ iterator, the second yielded
value is the list of substituted wildcard segments):

```go
// All Go files under any "cmd/*" sub-directory
for file, segments := range fs.MustGlob("cmd/*/*.go") {
	fmt.Println(segments, file.Path()) // segments == ["mytool", "main.go"]
}

// Relative to a specific base directory
iter, err := dir.Glob("**/*.png")
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

Encoding files with `encoding/gob`
----------------------------------

`File` implements `gob.GobEncoder` / `gob.GobDecoder`. Unlike the default
string marshaling (which only encodes the path/URI), gob encoding includes
the file's **content** so the receiver can rematerialize the bytes:

```go
var buf bytes.Buffer
err := gob.NewEncoder(&buf).Encode(fs.File("/tmp/data.bin"))

// On the receiver, decoding into a File writes the bytes to that file.
var dst fs.File = fs.TempDir().Join("decoded.bin")
err = gob.NewDecoder(&buf).Decode(&dst)
```

`MemFile` implements the same interfaces and round-trips its name and
data through gob without touching any file system.

Symbolic links
--------------

File systems opt into symbolic link support by implementing the
`SymbolicLinkFileSystem` interface. `LocalFileSystem` opts in; the cloud and
archive backends (s3fs, httpfs, ftpfs, sftpfs, zipfs, dropboxfs, multipartfs,
memfilesystem) do not, so calling these methods on files from those backends
returns an `ErrUnsupported` error.

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
backends. Each backend is an independent Go module under its own
sub-directory. Importing the package registers a `FileSystem` for its
URI prefix, after which `File` values with that prefix transparently
route to the right backend.

| Package         | URI prefix       | Constructor                                      | Read | Write |
| --------------- | ---------------- | ------------------------------------------------ | :--: | :---: |
| (built-in)      | `file://`        | `fs.Local` (registered by default)               | yes  | yes   |
| `httpfs`        | `http://`, `https://` | side-effect import: `import _ ".../httpfs"`  | yes  | no    |
| `s3fs`          | `s3://<bucket>`  | `s3fs.NewAndRegister` / `s3fs.NewLoadDefaultConfig` | yes | yes/ro |
| `sftpfs`        | `sftp://`        | `sftpfs.Dial` / `sftpfs.DialAndRegister`         | yes  | yes   |
| `ftpfs`         | `ftp://`         | `ftpfs.Dial` / `ftpfs.DialAndRegister`           | yes  | yes   |
| `dropboxfs`     | `dropbox://`     | `dropboxfs.NewAndRegister`                       | yes  | yes   |
| `zipfs`         | `zip://`         | `zipfs.NewReaderFileSystem` / `NewWriterFileSystem` | yes/ro | yes/ro |
| `multipartfs`   | (per-request)    | `multipartfs.FromRequestForm`                    | yes  | no    |
| (built-in)      | `mem://`         | `fs.NewMemFileSystem`                            | yes  | yes   |

### httpfs

```go
import _ "github.com/ungerik/go-fs/httpfs"

data, err := fs.File("https://example.com/file.txt").ReadAll(ctx)
```

Read-only. Useful for treating remote files uniformly with local ones.

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
5/10 MB.

### sftpfs

```go
import "github.com/ungerik/go-fs/sftpfs"

sftpFS, err := sftpfs.DialAndRegister(
    ctx,
    "sftp://user@host:22/",
    sftpfs.Password("secret"),
    ssh.InsecureIgnoreHostKey(), // use a real callback in production
    nil,
)

data, err := fs.File("sftp://user@host:22/etc/hostname").ReadAll(ctx)
```

### ftpfs

```go
import "github.com/ungerik/go-fs/ftpfs"

ftpFS, err := ftpfs.DialAndRegister(
    ctx,
    "ftp://example.com:21/",
    ftpfs.AnonymousCredentials,
    nil, // debug output
)
```

Supports plain FTP and FTPS.

### dropboxfs

```go
import "github.com/ungerik/go-fs/dropboxfs"

dbxFS := dropboxfs.NewAndRegister(accessToken, 5*time.Minute, false)

err := fs.File("dropbox://Apps/MyApp/notes.md").WriteAllString(ctx, "...")
```

### zipfs

```go
import "github.com/ungerik/go-fs/zipfs"

// Read a ZIP archive as a file system
zipFS, err := zipfs.NewReaderFileSystem(fs.File("archive.zip"))
defer zipFS.Close()

err = zipFS.RootDir().ListDir(func(f fs.File) error {
    fmt.Println(f.Path())
    return nil
})

// Write a new ZIP archive
out, err := zipfs.NewWriterFileSystem(fs.File("out.zip"))
defer out.Close()
```

A `ZipFileSystem` is either reader- or writer-mode depending on the
constructor used.

### multipartfs

See the introduction for `multipartfs.FromRequestForm` — it wraps an
uploaded HTML form so files can be consumed using the regular `fs.File`
API.

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

The implementation is mostly complete; a few uncommon operations may
still panic — see `memfilesystem.go` for current coverage.