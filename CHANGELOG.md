# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses Go's `vMAJOR.MINOR.PATCH` tag scheme.

## v1.0.0-beta.1 - 2026-09-06

First beta of the v1 API; further beta releases iterate on it before
v1.0.0 freezes the API. Upgrading from v0.x is mechanical, see
`docs/MIGRATION_v1.md`; the design decisions are recorded in
`docs/V1_ROADMAP.md`. All modules (`s3fs`, `sftpfs`, `ftpfs`, `dropboxfs`,
`tools`) are tagged in lockstep.

### Changed

- **`FileSystem` interface redesigned (Phase 3).** The core interface holds
  the primitives only: `ID() string`, `Prefix`, `Name`, `String`, `Separator`,
  `ReadableWritable`, `RootDir`, `CleanPath`, `Stat(path) (*FileInfo, error)`,
  `ListDir(ctx, dirPath, patterns, callback)`, `OpenReader(path) (io.ReadCloser,
  error)`, `Close`. Writing moved to `WriteFileSystem` (`OpenWriter`, `MakeDir`,
  `Remove`); read-only file systems implement no write stubs, the package
  returns `ErrReadOnlyFileSystem` / `ErrWriteOnlyFileSystem` itself based on
  `ReadableWritable`. Permissions are a single `Permissions` value (zero means
  the file system default). Removed from the interface: `URL`,
  `CleanPathFromURI`, `JoinCleanFile`, `JoinCleanPath`, `SplitPath`,
  `SplitDirAndName`, `MatchAnyPattern`, `IsHidden` (`HiddenFileSystem`,
  default dot rule), `IsSymbolicLink` (part of `SymbolicLinkFileSystem`),
  `IsAbsPath`/`AbsPath` (`AbsPathFileSystem`, which also absorbs `RelPath`),
  `OpenReadWriter` (`ReadWriterFileSystem`). `ExistsFileSystem.Exists` returns
  `(bool, error)`, `ListDirRecursiveFileSystem.ListDirRecursive` takes the
  patterns before the callback, `CopyFileSystem.CopyFile` lost the buffer
  parameter, `MoveFileSystem.Move` always gets the final destination path
  (`File.MoveTo` resolves "into directory"), new `RemoveAllFileSystem` and
  `PrefixAliasFileSystem` (sftpfs/ftpfs default ports). `ReadOnlyBase` and
  `FullyFeaturedFileSystem` are gone. `FileInfo` gained `IsSymlink` and `Sys`.
  `ParseRawURI` decodes URL escapes only for URIs with a scheme, so a local
  file literally named `a%20b` is reachable.
- `LocalFileSystem.ID()` returns the real id of the root file system (statfs
  `f_fsid` on Unix, the volume serial number on Windows); `Stat` follows
  symbolic links and reports `IsSymlink`; the default permissions come from the
  receiver instead of the `Local` singleton; `MakeAllDirs` on an existing file
  reports `ErrIsNotDirectory`; `Watch` expands a leading `~`.
- `File.Touch` on a file system without native touch creates a missing file and
  returns `ErrUnsupported` for an existing one instead of truncating it;
  `File.RemoveRecursive` no longer fails for a missing path and uses native
  `RemoveAll` where available; `File.IsSymbolicLink` is false on file systems
  without symbolic link support.

- **`File`, `FileReader` and `MemFile` follow the context rule (Phase 4).**
  A method takes `ctx` first only if it is a potentially long-running read or
  write: content transfer (`ReadAll`, `ReadAllString`, `ContentHash`,
  `WriteAll`, `WriteAllString`, `Truncate`, `MoveTo`), directory iteration
  (`ListDir*`, `Glob`, `MustGlob`, `RemoveRecursive`, `RemoveDirContents*`) and
  `TempFileCopy`, `uuiddir.Remove`, `uuiddir.RemoveDir`. The `*Context` twins
  are gone. See `docs/MIGRATION_v1.md` for the rename table and sed recipes.
- `File.IsWritable` is true for an existing writable directory as well;
  `StdFS` accepts every `io/fs.ValidPath` name (like `dir/.gitignore`);
  `MakeTempDir` uses `os.MkdirTemp`; `LocalFileSystem.Close` stops the watcher
  goroutine; `MemFileSystem` ids are random strings instead of heap addresses.
- The test suite is green on Windows and the Windows CI job is blocking:
  `StdFS` rejects names containing `\` or `:` on Windows like `os.DirFS`,
  `LocalFileSystem` XAttr methods return `ErrUnsupported` on platforms
  without extended attributes, `zipfs.NewWriterFileSystem` closes the
  underlying file on `Close` (the handle used to leak), and `fs.Glob` yields
  files cleaned for their file system's separator.

- **s3fs rework (Phase 5).** Object keys are derived consistently from the
  rooted file system paths (objects used to be written with a leading slash
  but listed without). `Stat` recognises marker and implicit directories,
  `MakeDir` on an existing path wraps `os.ErrExist`, `Remove` wraps
  `os.ErrNotExist` and refuses non-empty directories, `RemoveAll` uses
  batched `DeleteObjects`, `ListDirRecursive` is a single paginated listing,
  `OpenReader` streams the object body, `OpenReadWriter` creates a missing
  object, `CopyFile` URL-encodes the copy source. `MultipartUploadThreshold`
  and `MultipartDownloadThreshold` are variables now. The `Watch` stub and
  the `Exists` method are gone (the generic emulations cover both).
- **sftpfs rework (Phase 5).** A lost connection is reconnected with the
  stored credentials and host key callback and the operation retried once;
  the old reconnect code was unreachable and would have accepted any host
  key. URIs with embedded credentials (`sftp://user:pw@host/…`) now require
  `sftpfs.URLHostKeyCallback` to be set (use `sftpfs.AcceptAnyHostKey` for
  the old behaviour). Native `MakeAllDirs`, `ListDirRecursive`, `RemoveAll`,
  `SetPermissions` and symbolic link support; `Stat` reports symlinks; the
  `perm` argument is applied to created files and directories.
- **ftpfs rework (Phase 5).** `Dial`, `DialAndRegister` and
  `EnsureRegistered` take `*ftpfs.Options` (nil for defaults) instead of a
  debug writer; FTPS verifies the server certificate unless
  `Options.InsecureSkipVerify` is set; `ftps://` is explicit TLS on port 21
  and implicit TLS on port 990. Operations are serialised on the single
  control connection, a lost connection is reconnected and the operation
  retried once, `OpenReader` streams over a dedicated connection. FTP reply
  codes are checked instead of reply texts; `Remove` no longer tries `RMD`
  for a file that could not be deleted; `MakeDir` on an existing path wraps
  `os.ErrExist`; native `RemoveAll` and `ListDirRecursive`. The ftpfs tests
  run the conformance suite for FTP and FTPS against an in-process server
  on every platform; the dockerized vsftpd is gone.
- `httpfs` streams `OpenReader` from the GET body without a HEAD request
  first, uses `http.NewRequestWithContext` for `ReadAll`, and makes the
  `*http.Client` injectable via `httpfs.Client`. `multipartfs.EscapePath`
  (a stub that only replaced quotes) is removed. `CopyRecursive` creates
  missing destination directories with `MakeAllDirs`.
- **dropboxfs rework (Phase 5).** `NewAndRegister(ctx, token, cacheTimeout,
  mute)` fetches the account and returns an error; `ID()` and the prefix
  are derived from the account id instead of a random string. Only typed
  API errors are mapped to `os.ErrNotExist` / `os.ErrExist`; `Touch` of an
  existing file returns `ErrUnsupported`; `Remove` refuses a non-empty
  folder; native `RemoveAll`; `OpenReader` streams the download; the
  metadata cache is invalidated on writes.

### Added

- `fs.StdFileSystem` adapts any `io/fs.FS` (`embed.FS`, `os.DirFS`,
  `zip.Reader`, `testing/fstest.MapFS`) as a read-only file system with the
  prefix `stdfs://<id>`; the counterpart of `StdFS`.
- `fs.SubFileSystem` is a view of a directory of another file system with
  the prefix `sub://<id>`, forwarding every operation (including the
  optional interfaces) to the parent with translated paths.
- `webdavfs` module: a WebDAV client file system with the standard library
  only (`PROPFIND` for `Stat` and `ListDir`, `PUT`, `MKCOL`, `DELETE`,
  native `MOVE` and `COPY`, seeking reads with `Range` requests), tested
  against an in-process `golang.org/x/net/webdav` server.
- `fs.OverlayFileSystem` stacks a writable upper layer on a read-only base
  with the prefix `overlay://<id>`: reads fall through, listings are the
  union, writes go to the upper layer with copy-up for in-place changes,
  removed base entries are hidden by in-memory whiteouts.
- `fs.NewStdFileSystemWithPrefix` builds a `StdFileSystem` with a scheme of
  its own; zipfs uses it: `zipfs.ReaderFileSystem` is a `StdFileSystem` over
  `archive/zip.Reader` and `zipfs.WriterFileSystem` the sequential writer,
  replacing the mode-switching `ZipFileSystem` type. Reader listings are
  sorted by name and report the modes stored in the archive.
- `tarfs`: read-only and write-only file systems for tar archives,
  optionally gzip compressed, mirroring `zipfs` (root module, standard
  library only). `fsimpl.DirTree` is the directory tree of archive entries
  (formerly internal to zipfs).
- `fs.CreateTempFile` creates a temporary file atomically (`fs.TempFile` only
  returns a path).
- **`fstest.RunConformance`** replaces `fs.RunFileSystemTests` and the `tests`
  package. The suite seeds one directory tree, reads it back through the
  `FileSystem` methods and the `File` API on every backend including the
  read-only ones, verifies content (not just existence) for every write
  operation and optional interface, and checks the error contract:
  `os.ErrNotExist`, `os.ErrExist`, `ErrReadOnlyFileSystem`,
  `ErrWriteOnlyFileSystem`, `ErrFileSystemClosed`, context cancellation.
  httpfs, zipfs and multipartfs now run it too.
- CI runs build, vet, race tests, staticcheck and gosec for every module on
  ubuntu, macOS and Windows (Windows tests non-blocking for now).
- **`fsimpl.PathHelper`** implements the path methods of a `FileSystem`
  (prefix stripping, joining, cleaning, splitting, URL, hidden check) for a URI
  prefix, separator and optional volume; every backend embeds it instead of
  duplicating the same code. sftpfs and ftpfs now also resolve URIs that carry
  the default port (`sftp://u@h:22/x`, `ftp://h:21/x`, `ftps://h:990/x`).
- `fsimpl.NewWriteOnCloseFileBuffer` for file systems that upload whole files
  on Close; `fsimpl.FileBuffer.Truncate`.

### Fixed

- The generic `OpenAppendWriter` emulation (used by file systems without a
  native append writer) overwrote the beginning of the file instead of
  appending.
- `s3fs.DefaultDirPermissions` was `0660 + 0666`, which cleared the user
  write bit and made `File.IsWritable` false for new S3 objects.

- `JoinCleanPath` no longer modifies the passed slice (all file systems).
- `fsimpl.FileBuffer.WriteAt` no longer panics on a negative offset and honors
  the `io.WriterAt` contract; `Stat` of a buffer without a `FileInfo` returns an
  error instead of a nil `FileInfo`.
- `CleanPathFromURI` returns a cleaned path on every file system; `IsHidden`
  applies the dot rule on sftpfs, ftpfs and httpfs (was always false); `AbsPath`
  on sftpfs and ftpfs returns a rooted path instead of a URI.
- `MemFileSystem`: paths with the `\` separator are cleaned correctly,
  `Remove` refuses non-empty directories, `Stat`/`OpenReader`/`ReadAll` follow
  symbolic links, and operations after `Close` return `ErrFileSystemClosed`.
- httpfs: `Join` no longer produces `http:///host/...` URLs, and file infos
  are readable (`IsReadable` was always false).
- zipfs: listed files carry the `zip://` prefix, recursive listing lists files
  only and returns an error instead of panicking on conflicting entries,
  `Remove` reports `ErrReadOnlyFileSystem` and `Stat` reports
  `ErrFileSystemClosed` after `Close`.
- multipartfs: real sizes instead of `-1`, deterministic modification time,
  prefixed `FileInfo.File`, `ErrDoesNotExist`/`ErrIsNotDirectory` from listing,
  idempotent `Close`.
- sftpfs: `MakeDir` on an existing path wraps `os.ErrExist`, listing a file
  reports `ErrIsNotDirectory`.
- uuiddir: `Make` created `baseDir` instead of the UUID directory, `RemoveDir`
  accepted siblings sharing the path prefix (`/base` vs `/basement`), `Enum`
  aborted on one unparsable directory.

### Removed

- `fs.RunFileSystemTests` and the `tests` package (use `fstest.RunConformance`).
- `File.ListDirChan`, `File.ListDirRecursiveChan`, `File.GobEncode`/`GobDecode`
  (`FileReader` no longer requires `GobEncode`; `MemFile` keeps it), `fs.MemDir`,
  `fs.FileInfoCache`, all `*Context` method twins.
- `fs.ReadOnlyBase`, `fs.FullyFeaturedFileSystem`, `fs.RelPathFileSystem`
  (merged into `fs.AbsPathFileSystem`), `s3fs` `Watch` and `VolumeName` stubs,
  `fsimpl.DirEntryFromFileInfo`, `fsimpl.NewReadonlyFileBufferWithClose`,
  `fsimpl.ReadWriteAllSeekCloser.InvalidateBuffer` (unused).
- `fsimpl.DirEntryFromFileInfo`, `fsimpl.NewReadonlyFileBufferWithClose`,
  `fsimpl.ReadWriteAllSeekCloser.InvalidateBuffer` (unused).
- `fs.MemFileSystem.ReadAll` on a directory returns `ErrIsDirectory` instead
  of empty data.

## v0.1.0 - 2026-06-30

First release, and a v1.0 preparation pass: a broad audit of the library that
fixes data-loss and crash bugs across the backends, tightens the public API, and
adds cross-backend test coverage. `docs/V1_ROADMAP.md` tracks the remaining road to v1.0.

### Fixed

- **Overwriting a file with less data no longer leaves stale trailing bytes.**
  `File.WriteAll` (and `WriteAllString`/`WriteJSON`/`WriteXML`) fell back to a
  non-truncating writer on backends without a native `WriteAll` (sftpfs,
  ftpfs), so writing a shorter document over a longer one produced a corrupt
  mix of new and old bytes. The fallback now truncates.
- **Renaming a non-empty directory keeps its contents.** On backends without a
  native rename/move, `File.Rename` created an empty directory and discarded
  everything inside it. It now copies the whole tree and removes the source
  only after the copy succeeds.
- **FTP writes are correct.** ftpfs's random-access writer re-uploaded from
  scratch on every `Write` (so a multi-chunk write kept only the last chunk),
  leaked the FTP data response on every read, and never advanced the read
  offset. Writers now buffer and store once on `Close`, and `ReadAll` /
  `WriteAll` / `Append` use native RETR / STOR / APPE.
- **A flaky network no longer makes files look missing.** dropboxfs and httpfs
  reported every error (auth, rate-limit, 5xx, timeout) as "does not exist".
  They now surface the real error and report not-found only for an actual
  404 / `not_found`. httpfs also honors the HTTP status code, so `Exists()` and
  `OpenReader()` agree.
- **s3fs no longer panics** on S3-compatible servers that return a nil
  `ContentLength` or `LastModified`.
- **Closed file systems return `ErrFileSystemClosed`** instead of panicking.
  s3fs, sftpfs, ftpfs and dropboxfs guard every operation after `Close()`, and
  `Close` reliably unregisters and stops the auto-reconnect / lazy-dial path.
- **`EnsureRegistered` reference counting is correct** (sftpfs, ftpfs):
  releasing one reference no longer closes a connection that another caller
  still holds, and a connection that loses the dial-then-register race is
  discarded instead of closing the wrong instance.
- **`ContentHash` is correct for streamed readers.** `fsimpl.DropboxContentHash`
  now fills each 4 MB block with `io.ReadFull`, so a reader that returns data in
  smaller chunks produces the same hash as one large buffer.
- **`fsimpl.ReadWriteAllSeekCloser` no longer leaks the underlying file** — it
  gained the documented close callback and always runs it on `Close`.

### Added

- **Native `Touch` for sftpfs and ftpfs.** The generic `Touch` opens the file
  with `O_TRUNC` and would wipe an existing file; the native versions update the
  modification time in place (SFTP `SETSTAT`, FTP `MFMT`) and only create an
  empty file when one does not exist.
- **`fstest` package** with `MockFileSystem` / `MockFullyFeaturedFileSystem` for
  testing code that consumes the `FileSystem` interfaces (analogous to the
  standard library's `testing/fstest`).
- **Optional-interface support matrix** in the README, showing at a glance which
  backends natively implement `CopyFile`, `Move`, `Touch`, and so on.

### Changed

- **A URI with an unregistered scheme now resolves to the `Invalid` file
  system** (so the operation fails clearly) instead of being silently treated as
  a local path. A path with no scheme still resolves to the local file system.
  Import the backend to register its scheme, e.g.
  `import _ "github.com/ungerik/go-fs/s3fs"`.
- **zipfs writer mode is safe.** Writing zip entries out of order, or writing to
  a closed or superseded entry writer, now returns an error instead of silently
  corrupting the archive; only one entry writer may be open at a time.
- **`File` predicate methods are documented to never return errors.** `Exists`,
  `IsDir`, `IsReadable`, `IsWritable`, `IsRegular`, `IsEmptyDir` and
  `IsSymbolicLink` return the value consistent with the file not existing
  (`false`) on any error; use `CheckExists` / `CheckIsDir` / `Stat` when you need
  the error.

### Removed

- **`fs.MockFileSystem` and `fs.MockFullyFeaturedFileSystem` moved out of the
  root package** into the new `github.com/ungerik/go-fs/fstest` package.
  **Breaking:** update imports from `fs.MockFileSystem` to
  `fstest.MockFileSystem`.
- Dead code: the commented-out `subfilesystem.go` and `fsimpl/other.go`, the
  false `io/fs.GlobFS` claim on `StdFS`, and assorted commented-out blocks.

### For contributors

- The shared conformance suite (`RunFileSystemTests`) gained overwrite-shrink
  and non-empty-directory-rename coverage, and now registers the file system
  under test so the high-level `File` API paths run on every backend.
- ftpfs has a Docker-free in-process FTP test server; new closed-state and
  `EnsureRegistered` reference-count regression tests cover s3fs, sftpfs, ftpfs
  and dropboxfs.
