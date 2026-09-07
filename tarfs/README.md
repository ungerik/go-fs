# Tar File System

A [go-fs](https://github.com/ungerik/go-fs) file system for tar archives,
optionally gzip compressed (`.tar.gz`, `.tgz`): `tarfs.Reader` reads an
existing archive, `tarfs.Writer` creates a new one. The package mirrors
[zipfs](../zipfs/README.md).

Both work on any `fs.File`, so the archive itself can live on the local
disk, in memory, on S3 or on any other backend.

This package is part of the `github.com/ungerik/go-fs` module, no separate
`go get` is needed.

## Usage

### Reading

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/tarfs"
)

func main() {
    ctx := context.Background()

    tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"))
    if err != nil {
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
out, err := tarfs.NewWriter(fs.File("out.tgz"))
if err != nil {
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
  A gzip compressed archive is decompressed into memory first, because gzip
  streams can't be read at an offset. `Exists` and `ListDirRecursive` are
  native, the rest comes from the generic emulation of the `fs` package.
- **Writer.** Each file is buffered in memory until its writer is closed,
  because a tar header needs the size before the content. `Close` writes
  the end-of-archive marker and closes the gzip stream — an archive whose
  writer was not closed is incomplete.
- **Permissions.** Written entries use `tarfs.DefaultPermissions` /
  `tarfs.DefaultDirPermissions` unless permissions are passed explicitly.
- **URI prefix.** `tar://` followed by a random id, so several archives can
  be registered at the same time. `Close` unregisters the file system.
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
