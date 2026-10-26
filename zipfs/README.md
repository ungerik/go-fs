# ZIP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for ZIP archives:
`zipfs.Reader` reads an existing archive, `zipfs.Writer` creates a new one.
Both work on any `fs.File`, so the archive itself can live on the local
disk, in memory, on S3 or on any other backend. The package mirrors
[tarfs](../tarfs/README.md): both use the same rules for entry names,
duplicates and permissions, only zipfs converts entry names that are not
UTF-8 and `zipfs.Writer` rejects the names that are not valid in a zip
archive.

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
- **Entry types.** Regular files and directories are read. Symbolic links,
  devices and FIFOs are skipped: they don't exist in the file system and are
  missing when the archive is extracted with `fs.CopyRecursive`.
- **Entry names.** Backslashes become slashes and `..` elements that would
  leave the archive are removed when reading, like `archive/zip` does for its
  `io/fs` view, so no entry can be extracted outside of the destination. A
  later entry replaces an earlier one with the same path. A name without the
  UTF-8 flag of the zip format is taken from its Info-ZIP Unicode Path extra
  field if it has one, else a name that is not valid UTF-8, like the umlauts
  of old Windows tools, is converted from CP-437, the encoding of names
  without the flag; `archive/zip` returns the stored bytes.
- **Writer.** Each file is buffered in memory until its writer is closed,
  then it is compressed into the archive, so several files can be written at
  the same time. The writers implement `fs.WriteAborter`, `Abort` discards
  the file without an entry. A path can be written only once: a second write
  returns an error wrapping `os.ErrExist`, so an archive never gets duplicate
  entries. `MakeDir` writes a directory entry, so empty directories are kept.
  A path that is not valid in a zip archive returns an error wrapping
  `os.ErrInvalid`: a backslash, invalid UTF-8, a control character like
  NUL, a bidirectional formatting character that can disguise an extension,
  a colon, which marks a drive letter on Windows, or more than 65535 bytes.
  Paths are cleaned first, and names that only Windows can't extract, like
  `NUL` or `what?.txt`, are not checked, also not on Windows like by
  `fs.Zip`. The writer implements `fs.ValidNameFileSystem`, so a copy or
  move into it checks every name of the source first and fails before it
  writes anything.
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
