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

zipFS, err := zipfs.NewReader(fs.File("archive.zip"), "") // "" = random id
if err != nil {
    return err
}
defer zipFS.Close() // also unregisters the file system
// The File values of the archive resolve through the registry
if err := fs.Register(zipFS); err != nil {
    return err
}

err = zipFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
    fmt.Println(f.Path(), f.Size())
    return nil
})
if err != nil {
    return err
}

data, err := zipFS.RootDir().Join("docs", "readme.md").ReadAll(ctx)
```

`NewReader` and `NewWriter` don't register the file system. Register it before
you use the `fs.File` values of the archive, and `Close` it afterwards; the
`defer` before `fs.Register` also closes it when the registration fails.

`NewReader` takes a `fs.FileReader`, so the archive itself can live on any
backend:

```go
zipFS, err := zipfs.NewReader(fs.File("s3://bucket/archive.zip"), "")
```

A ZIP archive is read at the offsets of its entries, so `NewReader` opens it
with `OpenReadSeeker`. On file systems whose readers can seek — local files,
`MemFileSystem`, SFTP, SMB, WebDAV and Azure Blob Storage — the archive is read
in place. **The readers of S3, FTP, HTTP and Dropbox can't seek, so the whole
archive is read into memory first**: a 2 GB ZIP on S3 needs 2 GB of RAM. Copy
it to a local temp file first if that is too much.

`zipfs.Reader` provides `Exists`, `ReadAll` and `ListDirRecursive` natively
and synthesizes the directories implied by the entry names. Entries are
decompressed on demand. Only regular files and directories are exposed,
symbolic links, devices and FIFOs are skipped like by `tarfs` and
`fs.UnzipToMemFiles`.

## Write a ZIP archive

```go
out, err := zipfs.NewWriter(fs.File("out.zip"), "")
if err != nil {
    return err
}
defer out.Close()
if err := fs.Register(out); err != nil {
    return err
}

err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "hello")
if err != nil {
    return err
}
err = out.Close() // finishes the archive
```

A writer file system is **write-only**: reading from it returns
`fs.ErrWriteOnlyFileSystem`. Each file is buffered in memory until its writer
is closed and then compressed into the archive, so several files can be open
for writing at the same time. A writer from `OpenWriter` implements
`fs.WriteAborter`: `Abort` discards the file without writing an entry, which
`fs.CopyFile` does when a copy fails.

Call `Close` explicitly and check its error. The `defer` is a safety net, but
the archive's central directory is written by `Close`, so swallowing that error
gives you a truncated file. Calling `Close` twice is safe. Files whose writers
are still open when the archive is closed are not written.

## Read and write tar archives

`tarfs` mirrors `zipfs`. A gzip compressed archive is recognized by its
content when it is read, whatever its name. When writing, the file name decides:
a `.gz` or `.tgz` suffix means gzip, anything else is an uncompressed tar.

```go
import "github.com/ungerik/go-fs/tarfs"

tarFS, err := tarfs.NewReader(fs.File("backup.tar.gz"), "")
if err != nil {
    return err
}
defer tarFS.Close()
if err := fs.Register(tarFS); err != nil {
    return err
}

out, err := tarfs.NewWriter(fs.File("out.tgz"), "")
if err != nil {
    return err
}
defer out.Close()
if err := fs.Register(out); err != nil {
    return err
}
err = out.RootDir().Join("notes.txt").WriteAllString(ctx, "...")
if err != nil {
    return err
}
err = out.Close() // finishes the archive
```

Behaviours to plan for:

- A **gzip compressed archive is decompressed into memory** when opened, so
  reading a 10 GB `.tar.gz` needs 10 GB. An uncompressed `.tar` is indexed
  once and its content is read on demand, in place on file systems with
  seekable readers (see the ZIP section above).
- The **writer buffers each file until its writer is closed**, because tar
  needs to know the size before the content.
- **Only regular files and directories are exposed.** A hard link is read as a
  regular file with the content of the earlier entry it links to. Symbolic
  links, devices and FIFOs are skipped, so they are missing when you extract
  the archive with `fs.CopyRecursive`. Use `archive/tar` directly if you need
  them.

`tarfs.Reader` provides `Exists`, `ReadAll` and `ListDirRecursive` natively.

## Rules shared by zipfs and tarfs

Both packages read and write entries with the same rules:

- **Entry names are made safe when reading.** Backslashes become slashes and
  `..` elements that would leave the archive are removed, the way
  `archive/zip` does for its `io/fs` view, so an entry `..\..\evil.txt` is
  read as `evil.txt` and `fs.CopyRecursive` can't write outside of the
  destination. One unusual entry never makes the whole archive unreadable.
  zipfs takes a name without the UTF-8 flag from its Info-ZIP Unicode Path
  extra field if it has one that keeps the ASCII parts of the stored name,
  so it can't change an extension or directory, else it converts a name
  that is not valid UTF-8, like the umlauts of old Windows tools, from
  CP-437.
- **A later entry replaces an earlier one** with the same path, like
  extracting the archive would, also when one is a file and the other a
  directory.
- **A path is written only once.** Writing a file or directory that the
  archive already has, explicitly or as directory implied by another entry,
  returns an error wrapping `os.ErrExist`. A file below a file is
  `fs.ErrIsNotDirectory`, a file over a directory `fs.ErrIsDirectory`, and a
  path with a backslash `os.ErrInvalid`. So a written archive never has
  duplicate or conflicting entries. `MakeAllDirs` doesn't complain about
  existing directories. The zip writer also rejects the names that are not
  valid in a zip archive with `os.ErrInvalid`, with the same rules as
  `fs.Zip` below except for device names, which it doesn't reject on
  Windows; its paths are cleaned first.
- **Permissions and times.** Written entries get the current time and the
  passed permissions, or `DefaultPermissions` / `DefaultDirPermissions` of the
  package. Read entries report the permissions and modification time stored in
  the archive; entries without permission bits get the defaults.
  `FileInfo.Sys` is the `*tar.Header` or `*zip.FileHeader` of the entry.
- `MakeDir` writes a directory entry, so empty directories are kept.

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
files mix freely. The entry of a `MemFile` is named after its `FileName`
without a leading slash, which can be a relative path like `dir/a.txt`; every
other `FileReader` is named by `Name()` — the base name, not the path — so
files with the same base name from different directories collide. Rename first
if that matters:

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
An entry name that is not valid in a zip archive is an error wrapping
`os.ErrInvalid`, so `fs.UnzipToMemFiles` can unzip every archive they write
on the same operating system: an empty
name, a name that `fs.UnzipToMemFiles` would reject because it is absolute,
escapes with `..` or contains a backslash, a name that is not valid UTF-8 or
contains a control character, a bidirectional formatting character that
can disguise an extension (`evil\u202etxt.exe`) or a colon (a drive letter
on Windows), and a name longer than 65535 bytes. The `FileName` of a
`MemFile` is cleaned first; the name of another `FileReader` has to be a
clean slash separated path (`io/fs.ValidPath`). `archive/zip` itself writes
all of them, except a name that is too long, which it rejects after adding it
to the central directory. Names that only Windows can't extract, like
`what?.txt`, `name.` or names that differ only in case, are not checked, and
device names like `NUL` only on Windows, where `fs.UnzipToMemFiles` rejects
an archive with such a name.

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
so it may contain slashes. A name that the archive stores in CP-437, like the
umlauts of old Windows tools, is converted to UTF-8, or taken from its Info-ZIP
Unicode Path extra field if that keeps the ASCII parts of the stored name.
Names are not checked like the names `fs.Zip`
writes, so a name with a colon or a control character can't be zipped again.

This reads every entry into memory at once. For a large archive use
`zipfs.NewReader` and pull out only what you need.

## Extract an archive to disk

```go
zipFS, err := zipfs.NewReader(fs.File("archive.zip"), "")
if err != nil {
    return err
}
defer zipFS.Close()
if err := fs.Register(zipFS); err != nil {
    return err
}

err = fs.CopyRecursive(ctx, zipFS.RootDir(), fs.File("/srv/extracted"))
```

`CopyRecursive` creates missing destination directories and works across file
systems, so this also extracts straight into S3, SFTP or a `MemFileSystem`.
The extracted files keep the read, write and execute bits of their entries and
also get the owner's write permission; the setuid, setgid and sticky bits are
dropped, so extracting as root never creates setuid files.

## Verification

```go
zipFS, err := zipfs.NewReader(fs.File("out.zip"), "")
if err != nil {
    return err
}
defer zipFS.Close()
if err := fs.Register(zipFS); err != nil {
    return err
}

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

**`file already exists` when writing** — the path was already written, or a
file was written below it so it exists as a directory. Archives are written
sequentially, an entry can't be replaced.

**The archive's files are "invalid"** — the file system was not registered.
Call `fs.Register` after `NewReader` or `NewWriter`.

**Out of memory on a `.tar.gz` or on an archive in S3** — gzip archives are
decompressed into memory when opened, and archives on backends without seekable
readers are read into memory. Use an uncompressed `.tar`, copy the archive to a
local file first, or stream with `archive/tar` directly.

**Symbolic links are missing after extracting a tar** — `tarfs` doesn't expose
them, see above.

**Entries collide in `fs.Zip`** — entries of `File` values and other
non-`MemFile` readers are named by `Name()`, the base name. Use
`fs.FileReaderWithName` to give them distinct names.

**Modification times are missing** — `fs.Zip` records the `ModTime` of each
reader's `Stat()`, which is zero for a `MemFile`.

## Related

- [Serve files over HTTP](howto-serve-files-over-http.md) — zip on the fly for a download
- [Copy, move and compare](howto-copy-move-and-compare.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — what the archive backends implement
- [Handle form uploads](howto-handle-form-uploads.md)
