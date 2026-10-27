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
- **A failed upload commits nothing.** When reading the source fails or the
  context is canceled, a destination writer that implements `fs.WriteAborter`,
  like an S3, Azure, Dropbox, WebDAV or FTP upload, is aborted, so no partial
  data replaces an existing file. Local, in-memory, SFTP and SMB files are
  written in place, so a failed copy can leave them partial.

When source and destination are on the same file system and it implements
`CopyFileSystem`, the copy is server-side — S3, Azure, Dropbox and WebDAV never
move the bytes through your process. Copying a file onto itself is a no-op,
on every file system also through a `SubFileSystem` or the layer of an
`OverlayFileSystem` that holds the file, compared by path, and for a local
file also through another path to it, like a symbolic link or a hard link,
which would otherwise truncate the file while it is read. Like the other checks of
the destination it is done by path before the destination is opened, so it
assumes that nobody else changes the destination during the copy.

When copying between two `File`s and you pass no explicit permissions, the
source's read, write and execute bits are used. The setuid, setgid and sticky
bits are never copied, pass them as `perm` if the copy needs them. A source on
a read-only file system, like an upload, an embedded file or an archive entry,
lacks write permissions because of its file system, so its copy also gets the
owner's write permission and can be changed.

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

Filter by file name — the patterns apply to the **name** of files and
symbolic links, not the whole path, and sub-directories are always copied:

```go
err := fs.CopyRecursive(ctx, fs.File("/srv/data"), fs.File("s3://bucket/backup"), "*.json", "*.yaml")
```

It creates the destination directory if it doesn't exist and merges into it if
it does: a file of the source replaces a file at the same path, a file over a
directory is an `fs.ErrIsDirectory` and a directory over a file an
`fs.ErrIsNotDirectory`. The destination directory itself may be a symbolic
link to a directory, but below it a link is never written through, which would
write outside of the destination: a file or directory over a symbolic link is
an error wrapping `os.ErrExist` (`fs.ErrAlreadyExists`) that names the link.
That happens when a second copy finds a link that the first copy created for a
link of the source that is a directory now, or when a link of the destination
and a file or directory of the source only differ in the case of their names
and the destination ignores case, like macOS and Windows do. The links are
checked by path before a file or directory is written, so this holds as long
as nobody else changes the destination during the copy: a link that another
process creates between the check and the write is written through. Copying
a directory into one of its own sub-directories is an error wrapping
`os.ErrInvalid`, on every file system also through a `SubFileSystem` or into
either layer of an `OverlayFileSystem` directory, and for a local directory
also through a symbolic link to a parent directory or a bind mount. One
buffer is used for all files, which get permissions like with `CopyFile`;
directories get the destination's defaults.

A file system that can't write some names, like a `zipfs.Writer` a name with
a colon or `s3fs` a name with a backslash, implements
`fs.ValidateNameFileSystem`. `CopyFile`, `CopyRecursive` and `Move` into it,
also through a `SubFileSystem` or `OverlayFileSystem`, check the new names of
the files and links they copy and of every directory, also the missing parent
directories, and fail with an error wrapping `os.ErrInvalid` before they write
anything. A copy or move within one file system keeps the names it holds.

A symbolic link to a file is followed: the copy is a file with the content
and permissions of the target. A symbolic link to a directory is never
followed, like by `filepath.WalkDir`, so a link cycle can't copy the tree into
itself and a link to `/` can't copy the whole disk:

- Within one file system that implements `fs.SymbolicLinkFileSystem`
  (`LocalFileSystem`, `MemFileSystem`, `sftpfs`, `smbfs`, a `SubFileSystem` of
  one of them), the link is copied as a link with its target read from the
  source and stored verbatim, and it replaces a file or link at the same path
  of the destination.
- Between file systems the same target can point to another directory: an
  absolute target of a `SubFileSystem` is a path of its view, which the host
  resolves from its own root, and even a relative target that stays inside
  the copied directory can leave the copy, because the destination resolves
  its names with its own rules. With the links `a/b/sub -> ../..` and
  `a/b/x -> sub/../../..` the destination follows `sub` before the `..`
  after it, so the copy of `x` points three levels above the copy.
  A case-insensitive destination or one with another separator matches the
  names of a target differently too. So a link to a directory is never copied
  to another file system, which means another instance, also between two
  `MemFileSystem`s or between a `SubFileSystem` and its parent.
- Then, and within one file system that can't create links, `CopyRecursive`
  fails before it writes anything, with an error wrapping
  `errors.ErrUnsupported` that names the link. To find such links it lists the
  source tree once more before the copy, which it does for every copy to
  another file system.

A link that you pass as the source itself is followed, its directory is copied.

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
never merged or deleted. A local destination that is the source itself under
another spelling of its name, like `/data/Reports` for the directory
`/data/reports` on a case-insensitive disk, is no directory to move into:
the source is renamed, like by `LocalFileSystem.Move`.

More behaviours to rely on:

- **Moving a file onto itself is a no-op** returning nil. This matters: it is
  what stops the copy-then-delete fallback from destroying a file when source
  and destination resolve to the same place, on every file system also
  through `SubFileSystem`s of one parent, compared by path. Moving an entry of
  an overlay's base layer to its path in the upper layer is an error wrapping
  `os.ErrInvalid`, because removing the source would remove the copy. The
  fallback also compares local paths as files, so two paths of the same file through bind mounts (like
  `docker run -v /data:/a -v /data:/b`), a symbolic link to a parent
  directory, a `SubFileSystem` or the local layer of an `OverlayFileSystem`
  are a no-op too, unless the name in the same directory only differs in
  case: then the file is renamed like by `LocalFileSystem.Move`.
  `LocalFileSystem.Move` of a local file onto another hard link of it removes
  the source link, like a rename onto any other file. A destination that the
  file system matches to the source's own directory entry, like another case
  or Unicode normalization of the name on macOS, renames the file. Neither
  removes the only link of a file. The copy then delete fallback can't tell
  these apart without renaming, so it refuses a move onto another hard link
  with an error wrapping `os.ErrInvalid`, and so does a case-only rename
  through an `OverlayFileSystem`, which supports only one case of a name.
- **Moving a symbolic link onto the file it points to** is an error wrapping
  `os.ErrInvalid` on the local and the in-memory file system, with the native
  `Move` of `sftpfs` and `smbfs` and with copy then delete, like
  `mv` reports "are the same file": the link would replace the file and then
  point to itself. That includes the local layers of an `OverlayFileSystem`,
  where the local file system can resolve a link to another path than the
  overlay, like `../layer/file` or `File` for `file` on a case-insensitive
  disk. Only a local file with other hard links, which keep it, is
  replaced by the link. On Windows, where the link count is unknown, the move
  is always refused, and so it is over SFTP and SMB, which report no link
  count either.
- **Moving a directory into itself** or one of its sub-directories is an error
  wrapping `os.ErrInvalid`, like `os.Rename`, on every file system also
  through a `SubFileSystem` or into either layer of an `OverlayFileSystem`
  directory, and for a local directory also through such another path to it.
  The same holds for `CopyRecursive`.
- **Symbolic links are moved, not what they point to.** Within one file system
  that implements `fs.SymbolicLinkFileSystem` a link stays a link and replaces
  an existing file or link at the destination; an `OverlayFileSystem`, which
  can't create links, copies the file a link points to. A link at the destination is replaced, not its target, and is
  put back if the copy fails. Between file systems a link to a file is
  followed: the file is copied and only the link is deleted. A link to a
  directory, the moved source itself or one below a moved directory, is never
  copied to another file system, like by `CopyRecursive`: the move fails
  before it copies anything with an error wrapping `errors.ErrUnsupported`
  that names the link. The destination is checked by path before the copy
  writes, so the rules about links at the destination assume that nobody else
  changes the destination during the move.

To rename in place, without moving between directories:

```go
renamed, err := file.Rename(ctx, "new-name.txt")
renamed, err = file.Renamef(ctx, "report-%d.pdf", year)
```

`Rename` takes a context because a backend without a native move copies the
file. An existing file with the new name is replaced, an existing directory is
an error wrapping `os.ErrExist`. `Rename` rejects a name that is empty, `.` or
`..`, or that contains a slash or the separator, with an error wrapping
`os.ErrInvalid` — use `Move` to change directories. So is a name that the local
file system would write to another entry, like `report.` on Windows, and a new
name that a file system implementing `fs.ValidateNameFileSystem` rejects.

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

The default is the Dropbox content hash algorithm, the value Dropbox stores as
`content_hash` in its file metadata. The hash is always computed from the
content, on every file system. To use a standard hash, replace it once at
startup:

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

**`file already exists`** from `Move` or `Rename` — the final path is an
existing directory: the new name of `Rename`, or the source's name inside the
destination directory of `Move`. A move never merges into or deletes a
directory; remove it first or pick another name.

**A failed move left a partial copy at the destination** — `Move` deletes the
source only after the copy succeeded, so the source is untouched. Remove the
partial copy before you retry: a directory at the destination would make the
retry move the source into it.

**Copying to S3 is slow for many small files** — each is a separate request.
`CopyRecursive` is sequential by design; parallelise at your call site if the
backend tolerates it.

**`ContentHash` downloads the whole file** — expected, the hash is computed
from the content on every backend. Compare sizes first, which is what
`IdenticalFileContents` already does.

**Two files hash differently after a copy** — you changed
`fs.DefaultContentHash` between the two calls. Set it once at startup.

## Related

- [Connect a remote backend](howto-connect-a-remote-backend.md)
- [Work with archives](howto-work-with-archives.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — which backends copy and move natively
- [Errors](reference-errors.md)
