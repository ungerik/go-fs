# ZIP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for ZIP archives:
`zipfs.Reader` reads an existing archive, `zipfs.Writer` creates a new one.
Both work on any `fs.File`, so the archive itself can live on the local
disk, in memory, on S3 or on any other backend. The package mirrors
[tarfs](../tarfs/README.md): both use the same rules for entry names,
duplicates and permissions.

This package is part of the `github.com/ungerik/go-fs` module, no separate
`go get` is needed.

## Usage

### Reading

```go
import (
    "context"
    "fmt"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/zipfs"
)

func main() {
    ctx := context.Background()

    zipFS, err := zipfs.NewReader(fs.File("archive.zip"), "") // "" = random id
    if err != nil {
        panic(err)
    }
    // Register the file system so that the fs.File values of the archive work
    if err := fs.Register(zipFS); err != nil {
        panic(err)
    }
    defer zipFS.Close() // unregisters the file system

    content, err := zipFS.RootDir().Join("docs", "readme.txt").ReadAllString(ctx)

    err = zipFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
        fmt.Println(f.Path())
        return nil
    })
}
```

### Writing

```go
out, err := zipfs.NewWriter(fs.File("out.zip"), "")
if err != nil {
    panic(err)
}
if err := fs.Register(out); err != nil {
    panic(err)
}

err = out.RootDir().Join("docs", "readme.txt").WriteAllString(ctx, "Hello, ZIP!")

// Close finishes the archive and unregisters the file system
err = out.Close()
```

`fs.Zip` and `fs.ZipMemFiles` in the root package are shortcuts that zip a
set of files into a byte slice without a file system.

## How ZIP concepts map to the file system

- **Two directions, two types.** A ZIP archive is either read or written,
  never both. `Reader` returns `fs.ErrReadOnlyFileSystem` for writes and
  `Writer` returns `fs.ErrWriteOnlyFileSystem` for reads, instead of
  claiming the file does not exist.
- **Reader.** The entries are indexed into a directory tree when the archive
  is opened, with the directories implied by the entry names. `Exists`,
  `ReadAll` and `ListDirRecursive` are native, the rest comes from the generic
  emulation of the `fs` package. Entries are decompressed on demand. The
  archive is opened with `OpenReadSeeker`, which reads the whole archive into
  memory if the file system of the archive has no seekable readers, like S3.
- **Entry names.** Backslashes become slashes and `..` elements that would
  leave the archive are removed when reading, like `archive/zip` does for its
  `io/fs` view, so no entry can be extracted outside of the destination. A
  later entry replaces an earlier one with the same path.
- **Writer.** Each file is buffered in memory until its writer is closed,
  then it is compressed into the archive, so several files can be written at
  the same time. The writers implement `fs.WriteAborter`, `Abort` discards
  the file without an entry. A path can be written only once: a second write
  returns an error wrapping `os.ErrExist`, so an archive never gets duplicate
  entries. `MakeDir` writes a directory entry, so empty directories are kept.
  `Close` writes the central directory — an archive whose writer was not
  closed is incomplete.
- **Permissions and times.** Written entries get the current time and the
  passed permissions, or `zipfs.DefaultPermissions` /
  `zipfs.DefaultDirPermissions`. Read entries report the stored ones,
  `FileInfo.Sys` is the `*zip.FileHeader`.
- **URI prefix.** `zip://` followed by the id passed to `NewReader` or
  `NewWriter`, or a random one, so several archives can be registered at the
  same time. `Close` unregisters the file system.
- **`ID()`.** There is no backing store with an identifier of its own, so the
  prefix of the archive (`zip://<id>`) is used, for a `Reader` and a `Writer`
  alike.

## Testing

The tests run the go-fs conformance suite against a reader and a writer
file system. The archive is written to a temporary directory, so no
network and no Docker are needed.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
