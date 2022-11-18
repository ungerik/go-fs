go-fs: A unified file system for Go
===================================

fs.File
-------

The package is built around a `File` type that is a string underneath
and interpets its value as local file-system path or as URI.

```go
type File string
```

As a string type it's easy to assign string literals and it can be const
which would be impossible if `File` was an interface or struct:

```go
const fileConst fs.File = "~/file.a"

var fileVar fs.File = "~/file.b"

fileVar = fileConst
fileVar = "~/file.c"
```

Handy to pass string literals of local paths or URI to functions:

```go
func readFile(f fs.File) { /* ... */ }

readFile("../my-local-file.txt")

// HTTP reading works out of the box
readFile("https://example.com/file-via-uri.txt")
```

As a string type `File` naturally marshals/unmarshals as string path/URI
without having to implementing marshalling interfaces.

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

size := file.Size() // int64, 0 for non existing or dirs
isDir := dir.IsDir()      // true
exists := file.Exists()   // true
fileIsDir := file.IsDir() // false
modTime := file.ModTime()
hash, err := file.ContentHash()
regular := file.Info().IsRegular // true
info := file.Info().FSFileInfo() // io/fs.FileInfo
```

fs.FileReader
-------------

For cases where a file should be passed only for reading,
it's recommended to use the interface type `FileReader`.
It has all the read related methods of `File`, so a `File` can be assigned
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
its simple nature as just an wrapper of a name and some bytes.

```go
type MemFile struct {
	FileName string
	FileData []byte
}
```

The type exists because it's very common to build up a file in memory
and/or pass around some buffered file bytes together with a filename:

```go
func readFile(f fs.FileReader) { /* ... */ }

readFile(fs.NewMemFile("hello-world.txt", []byte("Hello World!")))

// Read another fs.FileReader into a fs.MemFile
// to have it buffered in memory
memFile, err := fs.ReadMemFile(ctx, fs.File("../my-local-file.txt"))

// Read all data similar to io.ReadAll from an io.Reader
var r io.Rader
memFile, err := fs.ReadAllMemFile(cxt, r, "in-mem-file.txt")
```

Note that `MemFile` is not a `File` because it doesn't have a path or URI.
The in-memory `FileName` is not interpreted as path and should not contain
path separators.