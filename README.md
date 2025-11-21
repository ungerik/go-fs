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

file2 := file.Dir().Join("a", "b", "c").Joinf("file%d.txt", 2)
path2 := file2.Path() // "/tmp/a/b/c/file2.txt"

abs := fs.File("~/some-dir/../file").AbsPath() // "/home/erik/file"
```

Meta information:

```go
size := file.Size() // int64, 0 for non existing or dirs
isDir := dir.IsDir()      // true
exists := file.Exists()   // true
fileIsDir := file.IsDir() // false
modTime := file.ModTime()
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

Listing directories
-------------------

```go
// Print names of all JPEGs in dir
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
```

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