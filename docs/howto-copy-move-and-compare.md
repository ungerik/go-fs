# How to copy, move and compare files

Move data between any two file systems — local to S3, SFTP to memory, ZIP to
disk — and check whether two files or directories hold the same content.

## Prerequisites

- `github.com/ungerik/go-fs` in your `go.mod`
- The backends you are using imported and registered, see
  [Connect a remote backend](howto-connect-a-remote-backend.md)

## Copy a single file

```go
err := fs.CopyFile(ctx, src, dest)
```

`src` is a `fs.FileReader`, so it can be a `File` on any backend or a
`MemFile`. `dest` is a `File`.

```go
err := fs.CopyFile(ctx, fs.File("/srv/report.pdf"), fs.File("s3://bucket/reports/report.pdf"))
err = fs.CopyFile(ctx, fs.NewMemFile("notes.txt", data), fs.File("sftp://user@host/tmp/notes.txt"))
```

Two conveniences worth knowing:

- **A destination that is an existing directory** receives a file with the
  source's base name. `fs.CopyFile(ctx, src, fs.File("/srv/out/"))` writes
  `/srv/out/report.pdf`.
- **Missing destination directories are created.** You do not need
  `MakeAllDirs` first.

When source and destination are on the same file system and it implements
`CopyFileSystem`, the copy is server-side — S3, Azure, Dropbox and WebDAV never
move the bytes through your process. Copying a file onto itself is a no-op.

When copying between two `File`s and you pass no explicit permissions, the
source's permissions are used.

### Reuse the buffer in a loop

`CopyFile` allocates a 4 MB buffer per call. Copying many files, hoist it:

```go
var buf []byte
for _, src := range sources {
    err := fs.CopyFileBuf(ctx, src, destDir.Join(src.Name()), &buf)
    if err != nil {
        return err
    }
}
```

The `*[]byte` must not be nil — `CopyFileBuf` panics on that, because it is a
programming error rather than a file system condition. The slice is allocated
on first use and reused after.

## Copy a directory tree

```go
err := fs.CopyRecursive(ctx, srcDir, destDir)
```

Filter by file name — the patterns apply to the **name**, not the whole path:

```go
err := fs.CopyRecursive(ctx, fs.File("/srv/data"), fs.File("s3://bucket/backup"), "*.json", "*.yaml")
```

It creates the destination tree as it goes, and refuses to copy a directory
over an existing file.

This is how you extract an archive, seed a `MemFileSystem` from disk, or sync a
directory to an object store:

```go
zipFS, err := zipfs.NewReader(fs.File("archive.zip"))
if err != nil {
    return err
}
defer zipFS.Close()

err = fs.CopyRecursive(ctx, zipFS.RootDir(), fs.File("/srv/extracted"))
```

## Move and rename

```go
err := fs.Move(ctx, source, destination)
err = source.MoveTo(ctx, destination) // same thing as a method
```

`Move` handles both files and directories, and it works **across** file
systems:

- Same file system: the native `Move` if the backend has one, otherwise copy
  then delete.
- Different file systems: `CopyRecursive` then `RemoveRecursive`.

Two behaviours to rely on:

- **Moving into an existing directory** appends the source's base name, so
  `fs.Move(ctx, f, fs.File("/srv/archive/"))` does what you expect.
- **Moving a file onto itself is a no-op** returning nil. This matters: it is
  what stops the copy-then-delete fallback from destroying a file when source
  and destination resolve to the same place.

To rename in place, without moving between directories:

```go
renamed, err := file.Rename("new-name.txt")
renamed, err = file.Renamef("report-%d.pdf", year)
```

`Rename` rejects a name containing the path separator — use `Move` to change
directories.

## Remove

```go
err := file.Remove()                          // one file or empty directory
err = dir.RemoveRecursive(ctx)                // directory and everything in it
err = dir.RemoveDirContents(ctx, "*.tmp")     // contents, keep the directory
err = dir.RemoveDirContentsRecursive(ctx)
```

For "delete if present", the package-level helpers already skip missing files:

```go
err := fs.Remove("/tmp/a.txt", "/tmp/b.txt")  // by URI
err = fs.RemoveFiles(fileA, fileB)            // by File
```

Or wrap a single call:

```go
err := fs.RemoveErrDoesNotExist(file.Remove())
```

`RemoveFile` exists as a callback for listing methods:

```go
err := dir.ListDir(ctx, fs.RemoveFile, "*.tmp")
```

## Compare files

**Same file?** A pure path comparison, no I/O:

```go
if fs.SameFile(a, b) { ... }
```

True when both resolve to the same file system and the same clean path.

**Same content?**

```go
identical, err := fs.IdenticalFileContents(ctx, fileA, fileB, fileC)
```

Takes two or more `FileReader`s; fewer is an error. It short-circuits: a
missing file is an error, and differing sizes return `false` without reading
anything. Files up to 16 MB are compared byte by byte in memory; larger ones
are compared by content hash so a big comparison does not exhaust RAM.

**Same directory?**

```go
identical, err := fs.IdenticalDirContents(ctx, dirA, dirB, true) // recursive
```

Compares entry names and sizes first, then content hashes. With `recursive ==
false`, sub-directories are ignored entirely.

## Content hashes

```go
hash, err := file.ContentHash(ctx)
data, hash, err := file.ReadAllContentHash(ctx) // one pass
```

The default is the Dropbox content hash algorithm, chosen so `dropboxfs` can
answer from metadata instead of downloading. To use a standard hash, replace it
once at startup:

```go
fs.DefaultContentHash = fs.ContentHashFuncFrom(sha256.New())
```

Every file compared must use the same function, so set it before any hashing
and do not change it at runtime.

Find a file by hash in a slice:

```go
i, err := fs.ContentHashIndex(ctx, files, hash) // -1 if not found
```

## Verification

```go
identical, err := fs.IdenticalDirContents(ctx, srcDir, destDir, true)
if err != nil {
    return err
}
if !identical {
    return errors.New("copy did not reproduce the source tree")
}
```

## Troubleshooting

**`can not copy a directory over a file`** — the destination exists and is not
a directory. Remove it or pick another path.

**A cross-file-system move left the source behind** — `Move` deletes the source
only after the copy succeeds. Check the returned error; a partial copy leaves
both sides in place on purpose.

**Copying to S3 is slow for many small files** — each is a separate request.
`CopyRecursive` is sequential by design; parallelise at your call site if the
backend tolerates it.

**`ContentHash` downloads the whole file** — expected, unless the backend can
answer from metadata. Compare sizes first, which is what
`IdenticalFileContents` already does.

**Two files hash differently after a copy** — you changed
`fs.DefaultContentHash` between the two calls. Set it once at startup.

**`CopyFileBuf: buf is nil` panic** — pass the address of a `[]byte` variable,
`&buf`, not a nil pointer.

## Related

- [Connect a remote backend](howto-connect-a-remote-backend.md)
- [Work with archives](howto-work-with-archives.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — which backends copy and move natively
- [Errors](reference-errors.md)
