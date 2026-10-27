# Tar File System

A [go-fs](https://github.com/ungerik/go-fs) file system for tar archives,
optionally gzip compressed (`.tar.gz`, `.tgz`): `tarfs.Reader` reads an
existing archive, `tarfs.Writer` creates a new one. The package mirrors
[zipfs](../zipfs/README.md): both use the same rules for entry names,
duplicates and permissions, only zipfs converts entry names that are not
UTF-8 and `zipfs.Writer` rejects the names that are not valid in a zip
archive.

Both work on any `fs.File`, so the archive itself can live on the local
disk, in memory, on S3 or on any other backend.

This package is part of the `github.com/ungerik/go-fs` module, no separate
`go get` is needed.

## Usage

### Reading

```go
import (
    "context"
    "fmt"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/tarfs"
)

func main() {
    ctx := context.Background()

    tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"), "") // "" = random id
    if err != nil {
        panic(err)
    }
    // Register the file system so that the fs.File values of the archive work
    if err := fs.Register(tarFS); err != nil {
        panic(err)
    }
    defer tarFS.Close() // unregisters the file system

    content, err := tarFS.RootDir().Join("etc", "hostname").ReadAllString(ctx)

    err = tarFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
        fmt.Println(f.Path())
        return nil
    })
}
```

### Writing

```go
// The .tar.gz or .tgz extension turns on gzip compression
out, err := tarfs.NewWriter(fs.File("out.tgz"), "")
if err != nil {
    panic(err)
}
if err := fs.Register(out); err != nil {
    panic(err)
}

err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "Hello, tar!")

// Close finishes the archive and unregisters the file system
err = out.Close()
```

## How tar concepts map to the file system

- **Two directions, two types.** A tar archive is a stream that is either
  read or written, never both. `Reader` returns
  `fs.ErrReadOnlyFileSystem` for writes and `Writer` returns
  `fs.ErrWriteOnlyFileSystem` for reads, instead of claiming the file does
  not exist.
- **Reader.** The archive is scanned once to index its entries into a
  directory tree; the content of a file is read from the archive on demand.
  A gzip compressed archive is recognized by its content, whatever its file
  name, and decompressed into memory first, because gzip streams can't be
  read at an offset. An uncompressed archive is opened with `OpenReadSeeker`,
  which reads it into memory if the file system of the archive has no
  seekable readers, like S3. `Exists`, `ReadAll` and `ListDirRecursive` are
  native, the rest comes from the generic emulation of the `fs` package.
- **Entry types.** Regular files and directories are read, a hard link is
  read as a regular file with the content of the earlier entry it links to.
  Symbolic links, devices and FIFOs are skipped: they don't exist in the
  file system and are missing when the archive is extracted with
  `fs.CopyRecursive`.
- **Entry names.** Backslashes become slashes and `..` elements that would
  leave the archive are removed when reading, like `archive/zip` does, so no
  entry can be extracted outside of the destination and one unusual entry
  never makes the whole archive unreadable. A later entry replaces an earlier
  one with the same path, also when one is a file and the other a directory.
- **Writer.** Each file is buffered in memory until its writer is closed,
  because a tar header needs the size before the content. The writers
  implement `fs.WriteAborter`, `Abort` discards the file without an entry.
  A path can be written only once: a second write returns an error wrapping
  `os.ErrExist`, so an archive never gets duplicate entries. `MakeAllDirs` is
  native and writes an entry for every missing directory. A path with
  a backslash, which is rejected so archives stay portable, with a NUL
  character, which a tar header can't hold, or that is not valid UTF-8, which
  the PAX format requires, returns an error wrapping `os.ErrInvalid`. The
  writer implements `fs.ValidateNameFileSystem`, so a copy or move into it
  checks the names it writes first and fails before it writes anything.
  `Close` writes the end-of-archive marker and closes the gzip stream — an
  archive whose writer was not closed is incomplete.
- **Permissions and times.** Written entries get the current time and the
  passed permissions, or `tarfs.DefaultPermissions` /
  `tarfs.DefaultDirPermissions`. Read entries report the permissions and
  modification time of their header, `FileInfo.Sys` is the `*tar.Header`.
- **URI prefix.** `tar://` followed by the id passed to `NewReader` or
  `NewWriter`, or a random one, so several archives can be registered at the
  same time. `Close` unregisters the file system.
- **`ID()`.** There is no backing store with an identifier of its own, so the
  prefix of the archive (`tar://<id>`) is used, for a `Reader` and a `Writer`
  alike.

## Testing

The tests run the go-fs conformance suite against a reader and a writer
file system, both uncompressed and gzip compressed. The archives are
written to a temporary directory, so no network and no Docker are needed.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
