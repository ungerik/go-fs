# FileSystem interfaces

Complete catalogue of the interfaces a go-fs backend can implement, and what
the package does for every operation a backend leaves out.

`filesystem.go` defines 27 interfaces: 26 file system interfaces and
`WriteAborter` for writers. A backend implements two file system interfaces
at most to be usable: `FileSystem` for reading, and `WriteFileSystem` if it
can write. The other 24 are *optional*: when a backend does not implement
one, the operation still works, built from the core primitives, or it is
refused with `ErrUnsupported` where nothing can be emulated. A backend
implements an optional interface only when it can do the job better than
that fallback.

For the reasoning behind this design see
[Optional interfaces and emulation](explanation-optional-interfaces.md).
For a guided walk-through of writing a backend see
[Implement a file system](tutorial-implement-a-filesystem.md).

## FileSystem (required)

Every backend implements this. Nothing works without it.

| Method             | Signature                                          | Notes                                              |
| ------------------ | -------------------------------------------------- | -------------------------------------------------- |
| `ID`               | `ID() string`                                      | A string that identifies this file system uniquely among the registered ones and never changes. Computed at construction, never blocks. Format is implementation specific, see [ID](#id). |
| `Prefix`           | `Prefix() string`                                  | URI prefix, e.g. `"file://"`, `"sftp://user@host"`. Must not be empty, see [the path contract](#the-path-contract). |
| `Name`             | `Name() string`                                    | Name of the implementation, like `"SFTP"`.         |
| `String`           | `String() string`                                  | Descriptive string for debug output, without passwords. |
| `Separator`        | `Separator() string`                               | Path separator.                                    |
| `ReadableWritable` | `ReadableWritable() (readable, writable bool)`     | Runtime direction. A backend that implements `WriteFileSystem` may still report `writable == false`. |
| `RootDir`          | `RootDir() File`                                   | Root directory, or `InvalidFile` for file systems without one (`httpfs`). |
| `CleanPath`        | `CleanPath(uriParts ...string) string`             | Joins and cleans; strips the prefix from the first part. Must not modify the passed slice. |
| `Stat`             | `Stat(filePath string) (*FileInfo, error)`         | Follows symbolic links. Wraps `os.ErrNotExist` when missing. |
| `ListDir`          | `ListDir(ctx, dirPath string, patterns []string, callback func(*FileInfo) error) error` | Non-recursive. The `FileInfo` of a symbolic link describes the entry itself, like `Lstat`. A callback error or a cancelled context stops the listing and is returned. |
| `OpenReader`       | `OpenReader(filePath string) (io.ReadCloser, error)` | `ErrIsDirectory` for a directory, from opening, `Read` or `Close`. |
| `Close`            | `Close() error`                                    | Unregisters the file system; every method returns `ErrFileSystemClosed` afterwards. Idempotent: calling it more than once returns nil. `fs.Local` only stops its watcher and stays usable. |

### ID

`ID()` is a string that identifies one file system uniquely among the
registered ones and never changes for its lifetime. It is computed at
construction and must never block, so it can be read from any code path.

Where the backing store has an identifier of its own, that one is used —
only two of the backends in this repository can: the local file system uses
the file system id of the root volume, dropboxfs the Dropbox account id.
Neither SFTP, FTP, WebDAV, SMB, S3 nor Azure Blob Storage exposes such an
identifier, so those use the coordinates that identify the store instead: the
bucket, the container, the share, or the user and host of the connection. The
in-memory, archive and form file systems have no backing store at all and use
the id passed to their constructor, or a generated one.

The format is therefore specific to the implementation and promises nothing
beyond uniqueness and stability. Use `ID()` to tell two file systems apart,
to key a cache, or as meta information; don't parse it. `Prefix()` is what
builds URIs and `Name()` is for human readable output. The value of every
file system is tabulated in the [README](../README.md#file-system-implementations).

## WriteFileSystem (required for writing)

| Method       | Signature                                          | Notes                                              |
| ------------ | -------------------------------------------------- | -------------------------------------------------- |
| `OpenWriter` | `OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error)` | Creates the file, or **truncates** an existing one. `ErrIsDirectory` for a directory. A writer that commits the data only in `Close` should implement `WriteAborter`. |
| `MakeDir`    | `MakeDir(dirPath string, perm Permissions) error`  | Single directory, no parents. Wraps `os.ErrExist` if the path exists, except where directories are implicit. |
| `Remove`     | `Remove(filePath string) error`                    | File or *empty* directory. Wraps `os.ErrNotExist` if missing, `ErrDirectoryNotEmpty` for a non-empty directory. |

A `perm` of zero means "the default permissions of the file system"; use
`Permissions.OrDefault` to apply your own default.

## WriteAborter

```go
type WriteAborter interface {
	io.WriteCloser
	Abort() error
}
```

Implemented by writers from `OpenWriter`, `OpenAppendWriter` and the
package's emulations that commit the written data only in `Close`, like
uploads to object stores. `Abort` discards the written data without committing
it and releases the writer; `Close` after `Abort` is a no-op returning nil.
`fs.CopyFile`, `File.ReadFrom`, the `WriteAll` emulation and the copy of a
move abort the writer when the copy fails or the context is canceled, and
otherwise return the error of `Close` joined with the copy error.

The writers of `s3fs`, `azureblobfs`, `dropboxfs`, `webdavfs` and `ftpfs`,
the entry writers of `zipfs` and `tarfs`, and `fsimpl.NewWriteOnCloseFileBuffer`
and `fsimpl.ReadWriteAllSeekCloser` implement it. Writers that write through,
like the ones of the local, memory, SFTP and SMB file systems, don't.

## Optional interfaces and their fallbacks

Each row is a complete contract: implement the interface to take over the
operation, or leave it out and get the fallback in the last column. The
fallback is never an error unless the column says so, so callers can use the
full `File` API against any backend.

Every optional interface embeds `FileSystem` or `WriteFileSystem`. The
setters of the permission, ownership, link and extended attribute interfaces
are only called on file systems that report themselves as writable.

### Reading, metadata and paths

| Interface                    | Method                            | Fallback when not implemented                      |
| ---------------------------- | --------------------------------- | -------------------------------------------------- |
| `ExistsFileSystem`           | `Exists`                          | `Stat`; an error wrapping `os.ErrNotExist` becomes `false`, any other error is returned. |
| `ReadAllFileSystem`          | `ReadAll`                         | `OpenReader` streamed into a buffer, context checked between reads. The returned slice belongs to the caller. |
| `ListDirRecursiveFileSystem` | `ListDirRecursive`                | `ListDir` plus recursion into sub-directories. Directories are not passed to the callback, and patterns match the file name only. Entries deleted concurrently are skipped. Symbolic links are never followed, like by `filepath.WalkDir`: every link, also one to a directory, is passed to the callback like a file with `IsSymlink` set and `IsDir` false. Only a link at the listed path is followed. Native implementations must follow the same rule. |
| `HiddenFileSystem`           | `IsHidden`                        | The name begins with a dot.                        |
| `VolumeNameFileSystem`       | `VolumeName`                      | `""`, the path has no volume.                      |
| `PrefixAliasFileSystem`      | `PrefixAliases`                   | No aliases; only `Prefix()` resolves to this file system. |
| `AbsPathFileSystem`          | `IsAbsPath`, `AbsPath`, `RelPath` | Every path counts as absolute, `AbsPath` returns the path unchanged, and `RelPath` is computed by the package from the split path elements. |
| `URIFileSystem`              | `PathFromURI`, `URIFromPath`, `SplitDirAndName` | The package decodes the URL escapes after the prefix and cleans the rest with `CleanPath`; the URI of a path is the prefix followed by the path with `%` escaped. For file systems whose paths are URIs in escaped form, like `httpfs`. |

### Writing

| Interface                | Method             | Fallback when not implemented                      |
| ------------------------ | ------------------ | -------------------------------------------------- |
| `WriteAllFileSystem`     | `WriteAll`         | `OpenWriter` (which truncates, so a shorter write leaves no stale trailing bytes) plus `Close`, or `Abort` on a failed write or a canceled context. |
| `AppendFileSystem`       | `Append`           | `OpenAppendWriter` if the backend has it, otherwise read the whole file and write it back with the data appended. |
| `AppendWriterFileSystem` | `OpenAppendWriter` | The file is read into an `fsimpl.FileBuffer` seeked to the end; `Close` writes the whole buffer back, `Abort` discards it. |
| `ReadWriterFileSystem`   | `OpenReadWriter`   | `fsimpl.ReadWriteAllSeekCloser`: the file is buffered in memory on first use and written back on `Close`. |
| `TruncateFileSystem`     | `Truncate(ctx, filePath, size)` | `Stat` then read-modify-write: cut to `size` or zero-pad up to it. A no-op if the size already matches; `ErrIsDirectory` for a directory; a negative size is an error. |
| `TouchFileSystem`        | `Touch`            | Creates the file if it is missing. On an **existing** file it returns `ErrUnsupported` — the fallback refuses to guess a modification time. |
| `MakeAllDirsFileSystem`  | `MakeAllDirs`      | `MakeDir` for each missing parent, deepest last. A concurrent creation that returns `os.ErrExist` is re-checked with `Stat` and tolerated. |
| `RemoveAllFileSystem`    | `RemoveAll`        | Recursive listing plus `Remove`. A path that does not exist is not an error, and entries removed concurrently are ignored. |
| `CopyFileSystem`         | `CopyFile`         | `OpenReader` streamed into `OpenWriter` through a 4 MiB buffer. `destFile` is always the final path, never a directory to copy into. |
| `MoveFileSystem`         | `Move(ctx, filePath, destPath)` | A copy of the file or directory tree, then the removal of the source after the copy succeeded. `destPath` is always the final path, never a directory to move into. |

There is no rename interface: `File.Rename` is a `Move` within the directory,
so it uses the native `Move` where the backend has one.

The overwrite semantics are the same for the native implementations and the
fallbacks, and the conformance suite checks them:

| Destination        | `Move` (and `File.Rename`)                         | `CopyFile`          |
| ------------------ | -------------------------------------------------- | ------------------- |
| missing            | proceeds                                           | proceeds            |
| existing file      | replaced, as by `os.Rename` (`ErrIsNotDirectory` if the source is a directory) | replaced |
| existing directory | an error wrapping `os.ErrExist` (`ErrAlreadyExists`), never merged or deleted | `ErrIsDirectory` |

A destination equal to the source is a no-op returning nil, like `os.Rename`.
Moving a directory into itself or one of its sub-directories is an error
wrapping `os.ErrInvalid`; the package checks it before it calls a native
`Move`. The package functions `fs.Move`, `File.MoveTo` and `fs.CopyFile`
resolve a destination that is an existing directory to "into the directory"
before these rules apply.

### Capabilities with no fallback

These return an error wrapping `errors.ErrUnsupported` when the backend does
not implement them, because there is nothing sensible to emulate.

| Interface                | Methods                                            | Behaviour without it                             |
| ------------------------ | -------------------------------------------------- | ------------------------------------------------ |
| `PermissionsFileSystem`  | `SetPermissions`                                   | `ErrUnsupported`                                 |
| `UserFileSystem`         | `User`, `SetUser`                                  | `ErrUnsupported`                                 |
| `GroupFileSystem`        | `Group`, `SetGroup`                                | `ErrUnsupported`                                 |
| `SymbolicLinkFileSystem` | `Lstat`, `CreateSymbolicLink`, `ReadSymbolicLink`  | `ErrUnsupported`; `File.Lstat` returns the same as `Stat` and `File.IsSymbolicLink` false |
| `XAttrFileSystem`        | `ListXAttr`, `GetXAttr`, `SetXAttr`, `RemoveXAttr` | `ErrUnsupported`                                 |
| `WatchFileSystem`        | `Watch`                                            | `ErrUnsupported`                                 |

`Lstat` describes a symbolic link itself, with `FileInfo.IsSymlink` set,
where `Stat` follows it. `SetXAttr` takes `XAttrFlags`: `XAttrCreate` fails
with an error wrapping `os.ErrExist` if the attribute exists, `XAttrReplace`
with an error wrapping `os.ErrNotExist` if it doesn't, and zero flags create
or replace it. `GetXAttr` and `RemoveXAttr` of a missing attribute also fail
with an error wrapping `os.ErrNotExist`, on every file system and platform.
The local file system emulates the flags on platforms whose extended
attributes have none.

`Watch` calls the callback of one watch serially, in the order of the events,
on a goroutine of the file system, so a slow callback only delays the later
events of the same watch. The events are the bitmask `Event` with the
constants `EventCreate`, `EventWrite`, `EventRemove`, `EventRename` and
`EventChmod`.

## Backend support matrix

Which backends implement which optional interface natively. The conformance
suite in `fstest` runs every native implementation against the same checks
as the emulation. `✓` native, `–` uses the fallback (works the same, just not
specialized), `r/o` read-only backend so the write operation does not apply.

| Capability             |  s3 | azblob | sftp | ftp | smb | dropbox | webdav | http |
| ---------------------- | :-: | :----: | :--: | :-: | :-: | :-----: | :----: | :--: |
| CopyFile (server-side) |  ✓  |   ✓    |  –   |  –  |  –  |    ✓    |   ✓    |  –   |
| Move                   |  –  |   –    |  ✓   |  ✓  |  ✓  |    ✓    |   ✓    |  –   |
| Exists                 |  –  |   –    |  –   |  –  |  –  |    –    |   –    |  ✓   |
| ReadAll                |  ✓  |   ✓    |  –   |  ✓  |  ✓  |    –    |   ✓    |  ✓   |
| WriteAll               |  ✓  |   ✓    |  –   |  ✓  |  ✓  |    ✓    |   ✓    | r/o  |
| Append                 |  –  |   –    |  –   |  ✓  |  –  |    –    |   –    | r/o  |
| OpenAppendWriter       |  –  |   –    |  ✓   |  ✓  |  ✓  |    –    |   –    | r/o  |
| OpenReadWriter         |  ✓  |   ✓    |  ✓   |  ✓  |  ✓  |    ✓    |   –    | r/o  |
| Touch                  |  ✓  |   ✓    |  ✓   |  ✓  |  ✓  |    –    |   –    | r/o  |
| Truncate               |  –  |   –    |  ✓   |  –  |  ✓  |    –    |   –    | r/o  |
| MakeAllDirs            |  –  |   –    |  ✓   |  –  |  ✓  |    –    |   –    | r/o  |
| RemoveAll              |  ✓  |   ✓    |  ✓   |  ✓  |  ✓  |    ✓    |   ✓    | r/o  |
| ListDirRecursive       |  ✓  |   ✓    |  ✓   |  ✓  |  –  |    ✓    |   –    |  –   |
| SetPermissions         |  –  |   –    |  ✓   |  –  |  ✓  |    –    |   –    | r/o  |
| Symbolic links         |  –  |   –    |  ✓   |  –  |  ✓  |    –    |   –    |  –   |
| Seeking reads          |  –  |   ✓    |  ✓   |  –  |  ✓  |    –    |   ✓    |  –   |

"Seeking reads" is not an interface: it means that the readers of
`OpenReader` implement `fs.ReadAtSeekCloser`, so `File.OpenReadSeeker` needs
no buffer. `sftpfs` and `ftpfs` return the prefix with the default port of
their protocol from `PrefixAliases`, so URIs with `:22` or `:21` resolve to
them too, and `httpfs` implements `URIFileSystem`.

`LocalFileSystem` and `MemFileSystem` implement almost every optional
interface. `LocalFileSystem` leaves `Exists` (a `Stat`) and
`ListDirRecursive` (no faster than the generic walk) to the fallback, and
implements `UserFileSystem` and `GroupFileSystem` only on Unix.
`MemFileSystem` implements `Exists`, `ListDirRecursive`, `UserFileSystem` and
`GroupFileSystem` too, and leaves out `HiddenFileSystem` and
`AbsPathFileSystem`, whose defaults fit it.

The views forward to the file systems they are built on: `SubFileSystem`
implements the reading, writing, copy, move, permission, link and watch
interfaces and passes them to its parent, which falls back where it has no
native implementation. `OverlayFileSystem` implements `ReadAll` and the
writing interfaces except copy and move, `StdFileSystem` provides `ReadAll`
and `ListDirRecursive`.

The archive and request-scoped backends implement a mode-dependent subset:
`zipfs.Reader` and `tarfs.Reader` provide `Exists`, `ReadAll` and
`ListDirRecursive`; the writers provide `Touch` and `MakeAllDirs`
(`tarfs.Writer` also `WriteAll`); `multipartfs` is read-only and provides
`Exists` and `ReadAll`.

## The path contract

The package guarantees that a backend is called with *clean paths* and never
with the prefix. A clean path is the output of `CleanPath`:

- cleaned, with `.` and `..` elements and duplicate separators removed
- using the file system's `Separator()`
- without the `Prefix()`
- absolute for the file system, starting with the separator or a volume, with
  the exception of file systems whose paths start with a host name (`httpfs`)

An empty path never reaches a backend; the package returns `ErrEmptyPath`.

The URI of a path is `Prefix()` followed by the path with every `%` escaped
as `%25` (`fsimpl.EscapeURIPath`), because `ParseRawURI` decodes the URL
escapes after the prefix. A file system that implements `URIFileSystem`
converts between URIs and paths itself and nothing is decoded.

A URI matches the prefix of a file system only at a boundary: the URI ends
after the prefix, the prefix ends with a slash (like `scheme://`), or the
prefix is followed by a slash or the separator of the file system. So the
prefix `s3://logs` matches `s3://logs/key`, but not `s3://logs-archive/key`.
The file system with the longest matching prefix or prefix alias wins, and
the URIs of every file system must satisfy the rule.

`fsimpl.PathHelper` implements all of this for you; see
[fsimpl reference](reference-fsimpl.md).

## The error contract

Implementations must return errors that satisfy `errors.Is` for:

| Condition                                  | Error                                                 |
| ------------------------------------------ | ----------------------------------------------------- |
| File does not exist                        | `os.ErrNotExist`                                      |
| `MakeDir` on an existing path              | `os.ErrExist`                                         |
| `Move` onto an existing directory          | `os.ErrExist`                                         |
| Operation needs a directory                | `ErrIsNotDirectory`, checked with `errors.Is(err, fs.ErrIsNotDirectory{})` |
| Operation cannot act on a directory        | `ErrIsDirectory`, checked with `errors.Is(err, fs.ErrIsDirectory{})` |
| `Remove` of a non-empty directory          | `ErrDirectoryNotEmpty`, checked with `errors.Is(err, fs.ErrDirectoryNotEmpty{})` |
| Directory moved into itself                | `os.ErrInvalid`                                       |
| Called after `Close`                       | `ErrFileSystemClosed`                                 |
| Context cancelled                          | the context error                                     |

Read-only and write-only file systems need no checks of their own: the package
returns `ErrReadOnlyFileSystem` and `ErrWriteOnlyFileSystem` based on what
`ReadableWritable` reports. See [Errors](reference-errors.md) for the full
catalogue.

## Related

- [Optional interfaces and emulation](explanation-optional-interfaces.md) — why the design works this way
- [Implement a file system](tutorial-implement-a-filesystem.md) — build one end to end
- [Run the conformance suite](howto-run-the-conformance-suite.md) — verify a backend against these contracts
- [fsimpl reference](reference-fsimpl.md) — helpers that implement most of this for you
- [Errors](reference-errors.md)
