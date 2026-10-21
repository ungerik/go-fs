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

The rules for the destination:

- **A destination that is an existing directory** receives a file with the
  source's base name. `fs.CopyFile(ctx, src, fs.File("/srv/out/"))` writes
  `/srv/out/report.pdf`.
- **An existing file is replaced**, an existing directory at the path of the
  copy is an `fs.ErrIsDirectory` error.
- **Missing destination directories are created.** You do not need
  `MakeAllDirs` first.
- **A failed copy commits nothing.** When reading the source fails or the
  context is canceled, a destination writer that implements `fs.WriteAborter`,
  like an S3 upload, is aborted, so no partial data replaces an existing file.

When source and destination are on the same file system and it implements
`CopyFileSystem`, the copy is server-side — S3, Azure, Dropbox and WebDAV never
move the bytes through your process. Copying a file onto itself is a no-op.

When copying between two `File`s and you pass no explicit permissions, the
source's permissions are used.

### Reuse the buffer in a loop

`CopyFile` allocates a 4 MiB buffer per call. Copying many files, allocate it
once and pass it to `CopyFileBuf`, like `io.CopyBuffer`:

```go
buf := make([]byte, 4<<20)
for _, src := range sources {
    err := fs.CopyFileBuf(ctx, src, destDir.Join(src.Name()), buf)
    if err != nil {
        return err
    }
}
```

An empty or nil `buf` makes `CopyFileBuf` allocate one, like `CopyFile`.

## Copy a directory tree

```go
err := fs.CopyRecursive(ctx, srcDir, destDir)
```

Filter by file name — the patterns apply to the **name**, not the whole path,
and sub-directories are always copied:

```go
err := fs.CopyRecursive(ctx, fs.File("/srv/data"), fs.File("s3://bucket/backup"), "*.json", "*.yaml")
```

It creates the destination directory if it doesn't exist and merges into it if
it does: a file of the source replaces a file at the same path, a file over a
directory is an `fs.ErrIsDirectory` and a directory over a file an
`fs.ErrIsNotDirectory`. Copying a directory into one of its own
sub-directories is an error wrapping `os.ErrInvalid`. One buffer is used for
all files.

This is how you extract an archive, seed a `MemFileSystem` from disk, or sync a
directory to an object store:

```go
zipFS, err := zipfs.NewReader(fs.File("archive.zip"), "")
if err != nil {
    return err
}
defer zipFS.Close()
err = fs.Register(zipFS)
if err != nil {
    return err
}

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
- Different file systems: copy then delete.

The source is only deleted after the copy completely succeeded, so a failed or
canceled copy never loses the source.

The destination rules:

| Destination        | Result                                                          |
| ------------------ | --------------------------------------------------------------- |
| missing            | the source is moved there                                       |
| existing directory | the source is moved **into** it, keeping its name               |
| existing file      | replaced, like by `os.Rename`; a directory source is an `fs.ErrIsNotDirectory` |

After an existing directory resolved to "into it", the path inside it is
checked with the same rules, and a directory that is already there under the
source's name is an error wrapping `os.ErrExist` (`fs.ErrAlreadyExists`): it is
never merged or deleted.

Two more behaviours to rely on:

- **Moving a file onto itself is a no-op** returning nil. This matters: it is
  what stops the copy-then-delete fallback from destroying a file when source
  and destination resolve to the same place.
- **Moving a directory into itself** or one of its sub-directories is an error
  wrapping `os.ErrInvalid`, like `os.Rename`.

To rename in place, without moving between directories:

```go
renamed, err := file.Rename(ctx, "new-name.txt")
renamed, err = file.Renamef(ctx, "report-%d.pdf", year)
```

`Rename` takes a context because a backend without a native move copies the
file. An existing file with the new name is replaced, an existing directory is
an error wrapping `os.ErrExist`. `Rename` rejects a name that is empty, `.` or
`..`, or that contains a slash or the separator, with an error wrapping
`os.ErrInvalid` — use `Move` to change directories.

## Remove

```go
err := file.Remove()                          // one file or empty directory
err = dir.RemoveRecursive(ctx)                // directory and everything in it
err = dir.RemoveDirContents(ctx, "*.tmp")     // files in it, keep the directory and sub-directories
err = dir.RemoveDirContentsRecursive(ctx)     // everything in it, keep the directory
```

`Remove` of a non-empty directory is an error matching
`errors.Is(err, fs.ErrDirectoryNotEmpty{})`.

For "delete if present", `fs.RemoveFiles` already skips missing files:

```go
err := fs.RemoveFiles(fileA, fileB)
```

Or wrap a single call:

```go
err := fs.RemoveErrDoesNotExist(file.Remove())
```

The method expression `fs.File.Remove` is a callback for the listing methods:

```go
err := dir.ListDir(ctx, fs.File.Remove, "*.tmp")
```

## Compare files

**Same file?** A pure path comparison, no I/O:

```go
if fs.SameFile(a, b) {
    fmt.Println("same file")
}
```

True when both resolve to the same file system and the same clean path.

**Same content?**

```go
identical, err := fs.IdenticalFileContents(ctx, fileA, fileB, fileC)
```

Takes two or more `FileReader`s; fewer is an error. It short-circuits: a
missing file is an error, and differing sizes return `false` without reading
anything. Files up to 16 MiB are compared byte by byte in memory; larger ones
are compared by content hash so a big comparison does not exhaust RAM.

**Same directory?**

```go
identical, err := fs.IdenticalDirContents(ctx, dirA, dirB, true) // recursive
```

Compares the files of both directories by name, size and content. With
`recursive == false`, sub-directories are ignored entirely. The sizes of
directories are not compared, they differ between file systems.

## Content hashes

```go
hash, err := file.ContentHash(ctx)
data, hash, err := file.ReadAllContentHash(ctx) // one pass
```

The default is the Dropbox content hash algorithm, chosen so `dropboxfs` can
answer from metadata instead of downloading. To use a standard hash, replace it
once at startup:

```go
fs.DefaultContentHash = fs.ContentHashFuncFrom(sha256.New)
```

`ContentHashFuncFrom` creates a new hash for every call, so the function can be
used concurrently. Every file compared must use the same function, so set it
before any hashing and do not change it at runtime.

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

**`file is not a directory`** from `CopyRecursive` or `Move` — a directory is
copied or moved onto an existing file. Remove the file or pick another path.

**`file already exists`** from `Move` or `Rename` — the destination is an
existing directory. A move never merges into or deletes a directory; remove it
first or move into it.

**A failed move left a partial copy at the destination** — `Move` deletes the
source only after the copy succeeded, so the source is untouched. Remove the
partial copy before you retry: a directory at the destination would make the
retry move the source into it.

**Copying to S3 is slow for many small files** — each is a separate request.
`CopyRecursive` is sequential by design; parallelise at your call site if the
backend tolerates it.

**`ContentHash` downloads the whole file** — expected, unless the backend can
answer from metadata. Compare sizes first, which is what
`IdenticalFileContents` already does.

**Two files hash differently after a copy** — you changed
`fs.DefaultContentHash` between the two calls. Set it once at startup.

## Related

- [Connect a remote backend](howto-connect-a-remote-backend.md)
- [Work with archives](howto-work-with-archives.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — which backends copy and move natively
- [Errors](reference-errors.md)
