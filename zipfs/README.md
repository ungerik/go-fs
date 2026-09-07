# ZIP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for ZIP archives:
`zipfs.Reader` reads an existing archive, `zipfs.Writer` creates a new one.
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
    "github.com/ungerik/go-fs/zipfs"
)

func main() {
    ctx := context.Background()

    zipFS, err := zipfs.NewReader(fs.File("archive.zip"))
    if err != nil {
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
out, err := zipfs.NewWriter(fs.File("out.zip"))
if err != nil {
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
- **Reader.** A `Reader` is a `fs.StdFileSystem` over the `io/fs.FS` of
  `archive/zip.Reader`, so it provides `ReadAll` and `ListDirRecursive` and
  gets the rest from the generic emulation of the `fs` package. The archive
  is opened as a read seeker and entries are decompressed on demand.
- **Writer.** Entries are written sequentially, so only one file of a
  writer can be open for writing at a time. `Close` finishes the central
  directory — an archive whose writer was not closed is incomplete.
- **URI prefix.** `zip://` followed by a random id, so several archives can
  be registered at the same time. `Close` unregisters the file system.
- **`ID()`.** There is no backing store with an identifier of its own, so the
  random id of the archive is used: `Reader.ID()` is that id,
  `Writer.ID()` is the whole prefix (`zip://<id>`).

## Testing

The tests run the go-fs conformance suite against a reader and a writer
file system. The archive is written to a temporary directory, so no
network and no Docker are needed.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
