# FileSystem interfaces

Complete catalogue of the interfaces a go-fs backend can implement, and what
the package does for every operation a backend leaves out.

A backend implements two interfaces at most to be usable: `FileSystem` for
reading, and `WriteFileSystem` if it can write. Every other interface in this
document is *optional*: when a backend does not implement it, the operation
still works, built from the core primitives. A backend implements an optional
interface only when it can do the job better than that fallback.

For the reasoning behind this design see
[Optional interfaces and emulation](explanation-optional-interfaces.md).
For a guided walk-through of writing a backend see
[Implement a file system](tutorial-implement-a-filesystem.md).

## FileSystem (required)

Every backend implements this. Nothing works without it.

| Method             | Signature                                          | Notes                                              |
| ------------------ | -------------------------------------------------- | -------------------------------------------------- |
| `ID`               | `ID() string`                                      | Stable identifier of the backing store, unique among registered file systems. Computed at construction, never blocks. |
| `Prefix`           | `Prefix() string`                                  | URI prefix, e.g. `"file://"`, `"sftp://"`. Must not be empty. |
| `Name`             | `Name() string`                                    | Name of the implementation.                        |
| `String`           | `String() string`                                  | Descriptive string for debug output.               |
| `Separator`        | `Separator() string`                               | Path separator.                                    |
| `ReadableWritable` | `ReadableWritable() (readable, writable bool)`     | Runtime direction. A backend that implements `WriteFileSystem` may still report `writable == false`. |
| `RootDir`          | `RootDir() File`                                   | Root directory, or `InvalidFile` for file systems without one (`httpfs`). |
| `CleanPath`        | `CleanPath(uriParts ...string) string`             | Joins and cleans; strips the prefix from the first part. Must not modify the passed slice. |
| `Stat`             | `Stat(filePath string) (*FileInfo, error)`         | Follows symbolic links. Wraps `os.ErrNotExist` when missing. |
| `ListDir`          | `ListDir(ctx, dirPath string, patterns []string, callback func(*FileInfo) error) error` | Non-recursive. A callback error or a cancelled context stops the listing and is returned. |
| `OpenReader`       | `OpenReader(filePath string) (io.ReadCloser, error)` |                                                    |
| `Close`            | `Close() error`                                    | Idempotent: calling it more than once returns nil. Unregisters the file system. File systems that can't be closed do nothing. |

## WriteFileSystem (required for writing)

| Method       | Signature                                          | Notes                                              |
| ------------ | -------------------------------------------------- | -------------------------------------------------- |
| `OpenWriter` | `OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error)` | Creates the file, or **truncates** an existing one. |
| `MakeDir`    | `MakeDir(dirPath string, perm Permissions) error`  | Single directory, no parents. Wraps `os.ErrExist` if the path exists, except where directories are implicit. |
| `Remove`     | `Remove(filePath string) error`                    | File or *empty* directory. Wraps `os.ErrNotExist` if missing, errors on a non-empty directory. |

A `perm` of zero means "the default permissions of the file system"; use
`Permissions.OrDefault` to apply your own default.

## Optional interfaces and their fallbacks

Each row is a complete contract: implement the interface to take over the
operation, or leave it out and get the fallback in the last column. The
fallback is never an error unless the column says so, so callers can use the
full `File` API against any backend.

### Reading and metadata

| Interface                    | Method                            | Fallback when not implemented                      |
| ---------------------------- | --------------------------------- | -------------------------------------------------- |
| `ExistsFileSystem`           | `Exists`                          | `Stat`; an error wrapping `os.ErrNotExist` becomes `false`, any other error is returned. |
| `ReadAllFileSystem`          | `ReadAll`                         | `OpenReader` streamed into a buffer, context checked between reads. |
| `ListDirMaxFileSystem`       | `ListDirMax`                      | `ListDir`, stopping once `max` entries were collected. `max == -1` means all. |
| `ListDirRecursiveFileSystem` | `ListDirRecursive`                | `ListDir` plus recursion into sub-directories. Directories are not passed to the callback, and patterns match the file name only. Entries deleted concurrently are skipped. |
| `HiddenFileSystem`           | `IsHidden`                        | The name begins with a dot.                        |
| `VolumeNameFileSystem`       | `VolumeName`                      | `""`, the path has no volume.                      |
| `PrefixAliasFileSystem`      | `PrefixAliases`                   | No aliases; only `Prefix()` resolves to this file system. |
| `AbsPathFileSystem`          | `IsAbsPath`, `AbsPath`, `RelPath` | Every path counts as absolute, `AbsPath` returns the path unchanged, and `RelPath` is computed by the package from the split path elements. |

### Writing

| Interface                | Method             | Fallback when not implemented                      |
| ------------------------ | ------------------ | -------------------------------------------------- |
| `WriteAllFileSystem`     | `WriteAll`         | `OpenWriter` (which truncates, so a shorter write leaves no stale trailing bytes) plus `Close`. |
| `AppendFileSystem`       | `Append`           | `OpenAppendWriter` if the backend has it, otherwise read the whole file and write it back with the data appended. |
| `AppendWriterFileSystem` | `OpenAppendWriter` | The file is read into an `fsimpl.FileBuffer` seeked to the end; `Close` writes the whole buffer back. |
| `ReadWriterFileSystem`   | `OpenReadWriter`   | `fsimpl.ReadWriteAllSeekCloser`: the file is buffered in memory on first use and written back on `Close`. |
| `TruncateFileSystem`     | `Truncate`         | `Stat` then read-modify-write: cut to `size` or zero-pad up to it. A no-op if the size already matches; `ErrIsDirectory` for a directory; a negative size is an error. |
| `TouchFileSystem`        | `Touch`            | Creates the file if it is missing. On an **existing** file it returns `ErrUnsupported` — the fallback refuses to guess a modification time. |
| `MakeAllDirsFileSystem`  | `MakeAllDirs`      | `MakeDir` for each missing parent, deepest last. A concurrent creation that returns `os.ErrExist` is re-checked with `Stat` and tolerated. |
| `RemoveAllFileSystem`    | `RemoveAll`        | Recursive listing plus `Remove`. A path that does not exist is not an error, and entries removed concurrently are ignored. |
| `CopyFileSystem`         | `CopyFile`         | `OpenReader` streamed into `OpenWriter` through a 4 MB buffer. |
| `MoveFileSystem`         | `Move`             | `CopyRecursive` followed by `RemoveAll`. `destPath` is always the final path, never a directory to move into. Moving a path onto itself must be a no-op returning nil, like `os.Rename`. |
| `RenameFileSystem`       | `Rename`           | `Move` within the same directory. A `newName` containing the separator is rejected. |

### Capabilities with no fallback

These return an error wrapping `errors.ErrUnsupported` when the backend does
not implement them, because there is nothing sensible to emulate.

| Interface                | Methods                                            | Behaviour without it                             |
| ------------------------ | -------------------------------------------------- | ------------------------------------------------ |
| `PermissionsFileSystem`  | `SetPermissions`                                   | `ErrUnsupported`                                 |
| `UserFileSystem`         | `User`, `SetUser`                                  | `ErrUnsupported`                                 |
| `GroupFileSystem`        | `Group`, `SetGroup`                                | `ErrUnsupported`                                 |
| `SymbolicLinkFileSystem` | `IsSymbolicLink`, `CreateSymbolicLink`, `ReadSymbolicLink` | `ErrUnsupported`; `IsSymbolicLink` returns false |
| `XAttrFileSystem`        | `ListXAttr`, `GetXAttr`, `SetXAttr`, `RemoveXAttr` | `ErrUnsupported`                                 |
| `WatchFileSystem`        | `Watch`                                            | `ErrUnsupported`                                 |

## Backend support matrix

Which backends implement which optional interface natively, as verified by the
conformance suite in `fstest`. `✓` native, `–` uses the fallback (works the
same, just not specialized), `r/o` read-only backend so the write operation
does not apply.

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

`LocalFileSystem` and `MemFileSystem` implement almost every optional
interface. What they leave to the fallback is what the fallback already does
best: `Exists` is a `Stat`, and the local file system has no recursive listing
faster than the generic walk. `MemFileSystem` additionally implements
`User`/`Group`, which the local file system only exposes on Unix.

The archive and request-scoped backends implement a mode-dependent subset:
`zipfs.Reader` is a `StdFileSystem` and provides `ReadAll` and
`ListDirRecursive`; `tarfs.Reader` provides `Exists` and `ListDirRecursive`;
the writers provide `Touch` (`tarfs.Writer` also `WriteAll`); `multipartfs` is
read-only and provides `Exists` and `ReadAll`.

## The path contract

The package guarantees that a backend is called with *clean paths* and never
with the prefix. A clean path is the output of `CleanPath`:

- cleaned, with `.` and `..` elements and duplicate separators removed
- using the file system's `Separator()`
- without the `Prefix()`
- absolute for the file system, starting with the separator or a volume, with
  the exception of file systems whose paths start with a host name (`httpfs`)

`Prefix() + path` is the URI of a path. `fsimpl.PathHelper` implements all of
this for you; see [fsimpl reference](reference-fsimpl.md).

## The error contract

Implementations must return errors that satisfy `errors.Is` for:

| Condition                           | Error                 |
| ----------------------------------- | --------------------- |
| File does not exist                 | `os.ErrNotExist`      |
| `MakeDir` on an existing path       | `os.ErrExist`         |
| Operation needs a directory         | `ErrIsNotDirectory`   |
| Operation cannot act on a directory | `ErrIsDirectory`      |
| Called after `Close`                | `ErrFileSystemClosed` |
| Context cancelled                   | the context error     |

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
