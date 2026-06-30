# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses Go's `vMAJOR.MINOR.PATCH` tag scheme.

## v0.1.0 - 2026-06-30

First tagged release, and a v1.0 preparation pass: a broad audit of the library
that fixes data-loss and crash bugs across the backends, tightens the public
API, and adds cross-backend test coverage. `TODOS.md` tracks the remaining road
to v1.0.

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
