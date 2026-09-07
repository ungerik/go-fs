# How to work with archives

Read a ZIP or tar archive as a file system, write a new one, or zip and unzip
in memory.

There are two separate tools here, and picking the wrong one is the usual
mistake:

- **`zipfs` / `tarfs`** mount an archive as a `FileSystem`, so you browse and
  stream it with the normal `File` API. Use these for large archives, or when
  you only want a few entries out of many.
- **`fs.Zip` / `fs.UnzipToMemFiles`** build or explode a ZIP entirely in
  memory. Use these when the archive is small and you want a `[]byte` or a
  `[]MemFile`.

## Prerequisites

- `github.com/ungerik/go-fs` in your `go.mod`
- `zipfs` and `tarfs` are packages of the **root module**, so no extra `go get`

## Read a ZIP archive

```go
import "github.com/ungerik/go-fs/zipfs"

zipFS, err := zipfs.NewReader(fs.File("archive.zip"))
if err != nil {
    return err
}
defer zipFS.Close()

err = zipFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
    fmt.Println(f.Path(), f.Size())
    return nil
})
if err != nil {
    return err
}

data, err := zipFS.RootDir().Join("docs", "readme.md").ReadAll(ctx)
```

`NewReader` takes a `fs.FileReader`, so the archive itself can live on any
backend — read a ZIP straight out of S3 without downloading it to disk first:

```go
zipFS, err := zipfs.NewReader(fs.File("s3://bucket/archive.zip"))
```

`zipfs.Reader` is a `fs.StdFileSystem` over the `io/fs.FS` of
`archive/zip.Reader`, which synthesizes the directories implied by entry names.
It provides `ReadAll` and `ListDirRecursive` natively.

## Write a ZIP archive

```go
out, err := zipfs.NewWriter(fs.File("out.zip"))
if err != nil {
    return err
}
defer out.Close()

err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "hello")
if err != nil {
    return err
}
err = out.Close() // finishes the archive
```

A writer file system is **write-only**: reading from it returns
`fs.ErrWriteOnlyFileSystem`. Because `archive/zip` writes entries sequentially,
only one file can be open for writing at a time.

Call `Close` explicitly and check its error. The `defer` is a safety net, but
the archive's central directory is written by `Close`, so swallowing that error
gives you a truncated file. Calling `Close` twice is safe.

## Read and write tar archives

`tarfs` mirrors `zipfs`. Compression is chosen by the file name: a `.gz` or
`.tgz` suffix means gzip, anything else is an uncompressed tar.

```go
import "github.com/ungerik/go-fs/tarfs"

tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"))
if err != nil {
    return err
}
defer tarFS.Close()

out, err := tarfs.NewWriter(fs.File("out.tgz"))
if err != nil {
    return err
}
err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "...")
if err != nil {
    return err
}
err = out.Close() // finishes the archive
```

Two behaviours to plan for:

- A **gzip compressed archive is decompressed into memory** when opened, so
  reading a 10 GB `.tar.gz` needs 10 GB. An uncompressed `.tar` is indexed
  once and its content is read on demand.
- The **writer buffers each file until its writer is closed**, because tar
  needs to know the size before the content.

`tarfs.Reader` provides `Exists` and `ListDirRecursive` natively.

## Zip in memory

```go
files := []fs.FileReader{
    fs.NewMemFile("a.txt", []byte("aaa")),
    fs.File("/srv/reports/b.pdf"),
}

data, err := fs.Zip(ctx, files...)
if err != nil {
    return err
}
```

`fs.Zip` takes any `FileReader`, so local files, remote files and in-memory
files mix freely. Entries are named by `Name()` — the base name, not the path —
so files with the same base name from different directories collide. Rename
first if that matters:

```go
renamed := fs.FileReaderWithName(fs.File("/srv/a/report.pdf"), "a-report.pdf")
```

To zip a directory listing:

```go
files, err := fs.File("/srv/reports").ListDirMax(ctx, -1, "*.pdf")
if err != nil {
    return err
}
data, err := fs.Zip(ctx, fs.AsFileReaders(files)...)
```

For `MemFile` values specifically there is a context-free variant, since
nothing can block:

```go
data, err := fs.ZipMemFiles(
    fs.NewMemFile("a.txt", []byte("aaa")),
    fs.NewMemFile("b.txt", []byte("bbb")),
)
```

Both use `flate.BestCompression` and build the whole archive in memory.

## Unzip in memory

```go
memFiles, err := fs.UnzipToMemFiles(ctx, fs.File("archive.zip"))
if err != nil {
    return err
}
for _, mf := range memFiles {
    fmt.Println(mf.FileName, len(mf.FileData))
}
```

Directory entries are skipped. `FileName` keeps the full path from the archive,
so it may contain slashes.

This reads every entry into memory at once. For a large archive use
`zipfs.NewReader` and pull out only what you need.

## Extract an archive to disk

```go
zipFS, err := zipfs.NewReader(fs.File("archive.zip"))
if err != nil {
    return err
}
defer zipFS.Close()

err = fs.CopyRecursive(ctx, zipFS.RootDir(), fs.File("/srv/extracted"))
```

`CopyRecursive` creates missing destination directories and works across file
systems, so this also extracts straight into S3, SFTP or a `MemFileSystem`.

## Verification

```go
zipFS, err := zipfs.NewReader(fs.File("out.zip"))
if err != nil {
    return err
}
defer zipFS.Close()

count := 0
err = zipFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
    count++
    return nil
})
fmt.Println(count, "entries")
```

```bash
unzip -l out.zip
tar -tzf out.tgz
```

## Troubleshooting

**`file system is write-only`** — you tried to read from a `NewWriter` file
system. Close it and open it with `NewReader`.

**The archive is truncated or unreadable** — `Close` was not called, or its
error was ignored. `Close` writes the central directory for ZIP and the end
blocks for tar.

**`previous zip entry writer must be closed before opening another`** —
`archive/zip` only allows writing to the most recently created entry, so close
the first writer before opening the second.

**Out of memory on a `.tar.gz`** — gzip archives are decompressed into memory
when opened. Use an uncompressed `.tar`, or stream with `archive/tar` directly.

**Entries collide in `fs.Zip`** — entries are named by `Name()`, the base name.
Use `fs.FileReaderWithName` to give them distinct names.

**Modification times are missing** — `fs.Zip` only records a time for readers
that have a `Modified()` method, which `MemFile` does not.

## Related

- [Serve files over HTTP](howto-serve-files-over-http.md) — zip on the fly for a download
- [Copy, move and compare](howto-copy-move-and-compare.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — what the archive backends implement
- [Handle form uploads](howto-handle-form-uploads.md)
