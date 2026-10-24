# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses Go's `vMAJOR.MINOR.PATCH` tag scheme.

## [Unreleased]

### Changed

- `WriteJSONIndent` and `WriteXMLIndent` of `File` and `MemFile`, and
  `NewMemFileWriteJSONIndent`/`NewMemFileWriteXMLIndent`, put every element on
  its own line without indentation when `indent` is empty, instead of writing
  the compact output of `WriteJSON`/`WriteXML`. For JSON that is the output of
  beta.1's `WriteJSON(ctx, v, "")` again. For XML the newlines are new and are
  read back as part of mixed content (character data next to child elements);
  `WriteXML` still adds no whitespace. A NUL byte, which is invalid in XML,
  directly after a newline in raw `,innerxml`, `,cdata` or `,comment` content
  is removed together with the indentation.

## [1.0.0-beta.2] - 2026-10-23

Second beta. It fixes the bugs of a full review of beta.1 (data loss,
security, crashes) and settles the API decisions that freeze with v1.0.0.
Most breaking changes fail to compile, so the compiler finds the call sites;
`docs/MIGRATION_v1.md` lists them in "Upgrading from v1.0.0-beta.1". All
modules are tagged in lockstep, the `tools` module is not tagged.

### Changed

- **Constructors never register implicitly.** `New…`/`Dial…` return the file
  system, and an error where construction can fail, and don't register it; `…AndRegister` variants register
  and return only an `error`. Every constructor that used to register under a
  plain name changed its signature, so old call sites don't compile instead of
  silently losing the registration: `fs.NewMemFileSystem(opts
  *MemFileSystemOptions, initialFiles...)` (ID, separator, volume and
  read-only are options; `WithID`, `WithVolume` and `NewSingleMemFileSystem`
  are gone), `zipfs`/`tarfs` `NewReader`/`NewWriter(file, id)`,
  `multipartfs.New(form, id)` and `multipartfs.NewFromRequest` (was
  `FromRequestForm`), `s3fs.NewLoadDefaultConfig` and
  `azureblobfs.NewFromConnectionString`. Temporary file systems are used with
  `New`, `fs.Register` and `defer f.Close()`.
- **Registry.** A URI matches a prefix only at a boundary (the URI ends, the
  prefix ends with `/`, or `/` or the separator follows), and the longest
  match over prefixes and aliases wins, so `s3://logs` no longer serves
  `s3://logs-archive/…` and `ftp://user@files` no longer serves
  `ftp://user@files.example.com/…`. `Register` returns an error
  (`ErrAlreadyRegistered`) when another file system holds the prefix and is
  not reference counted any more; `Unregister` and `IsRegistered` compare
  instances. `fs.EnsureRegistered` is an atomic get-or-create with one shared
  connect for concurrent callers and reference counted `free` functions.
  `Close` always unregisters.
- **Per-instance options in every backend, no mutable package config.**
  `s3fs.New(client, bucket, *Options)`, `azureblobfs.New(ctx, client,
  *Options)`, `dropboxfs.New(ctx, token, *Options)`, `sftpfs.Dial(ctx,
  address, *Options)` (password, public keys, agent and keyboard-interactive
  via `Auth`, host key callback that fails closed, timeouts, keep-alive,
  retries, `DialContext`), `ftpfs.Dial(ctx, address, *Options)` (credentials
  moved into the options), `smbfs` `Options.DialContext`. The package
  variables `s3fs.MultipartUploadThreshold`/`MultipartDownloadThreshold`,
  `sftpfs.MaxConnectRetries`/`InitialRetryBackoff`/`URLHostKeyCallback` and
  `httpfs.Client` are gone; all `DefaultPermissions`/`DefaultDirPermissions`
  are constants. `httpfs` has an exported `FileSystem` type with
  `New(prefix, *Options)` and `NewAndRegister`, for example to use an
  authenticating client for `https://api.example.com/`; its `Close` unregisters it,
  `Default` and `DefaultTLS` stay registered.
- **Error contract.** The typed errors (`ErrDoesNotExist`, `ErrPermission`,
  `ErrAlreadyExists`, `ErrIsDirectory`, `ErrIsNotDirectory` and the new
  `ErrDirectoryNotEmpty`) are created for a `FileReader`; `File()` returns
  `(File, bool)`, the new `FileReader()` the passed reader, and every typed
  error matches `errors.Is(err, fs.ErrIsDirectory{})` and so on. They stay
  comparable for non-comparable readers like `MemFile`, so `errors.Is` never
  panics. `ErrEmptyPath` is a constant that no longer wraps `os.ErrNotExist`,
  so `Remove("")` is an error. Files of unregistered prefixes report
  `ErrInvalidFileSystem` with the URI (password redacted) instead of
  "write-only". New sentinels `ErrFileSystemsDoNotMatch` and
  `ErrAlreadyRegistered`. Error messages never contain passwords, SAS
  signatures or URL queries.
- **Move, Rename and CopyFile onto an existing destination** behave the same
  on every file system: an existing file is replaced, an existing directory
  is an error wrapping `os.ErrExist` (`ErrIsDirectory` for `CopyFile`) and is
  never merged or deleted, a directory onto a file is `ErrIsNotDirectory`,
  moving a directory into itself is an error wrapping `os.ErrInvalid`.
- **Implementer interfaces.** `MoveFileSystem.Move` and
  `TruncateFileSystem.Truncate` take a ctx; `SymbolicLinkFileSystem` has
  `Lstat` instead of `IsSymbolicLink`; `XAttrFileSystem.SetXAttr` takes the
  portable `XAttrFlags` (`XAttrCreate`, `XAttrReplace`); `PermissionsFileSystem`
  embeds `FileSystem`; `RenameFileSystem` and `ListDirMaxFileSystem` are
  removed (Rename is a Move within the directory). New `WriteAborter`, which
  the package uses to discard uploads that commit on Close when a copy fails,
  and `URIFileSystem` for file systems whose paths are escaped URIs.
  `fsimpl.PathHelper` is no longer embedded as an exported field and lost
  `IsAbsPath`, `AbsPath` and `IsHidden`. `Event` has go-fs owned constants
  (`EventCreate`, …) instead of fsnotify values, and the callbacks of one
  watch are called serially in event order.
- **File API.** `File.Rename`/`Renamef` take a ctx (without native move they
  copy); `File.Lstat` is new and `ReadSymbolicLink` returns the raw target
  string; `FileReader` gains `Stat` and `IsSymbolicLink`; `Glob` yields
  `iter.Seq2[File, error]` with pattern, listing and ctx errors, `MustGlob` is
  removed; `WriteJSON`/`WriteXML(ctx, input, perm...)` and
  `WriteJSONIndent`/`WriteXMLIndent(ctx, input, indent, perm...)` (same for
  `MemFile` and `NewMemFileWrite*`); `ServeFileHTTP(w, r, file, contentType
  string)`; `TempFile(ext)`/`CreateTempFile(ext)`;
  `ContentHashFuncFrom(newHash func() hash.Hash)`; `CopyFileBuf(ctx, src,
  dest, buf []byte, perm...)`; `fs.ReadAll(ctx, r)`/`fs.WriteAll(ctx, w,
  data)` (were `ReadAllContext`/`WriteAllContext`); `ReadSeekCloser` is
  `ReadAtSeekCloser`.
- **fstest** no longer depends on testify, derives every name from the seed,
  has `Config.Skip` and many new contract checks (errors, closed file systems,
  cancellation, overwrite semantics, moves into itself, content above 1 MiB,
  `WriteAborter`, URI boundary and round trip). The Docker helpers moved to
  `internal/dockertest`.
- `s3fs` uses `feature/s3/transfermanager` instead of the deprecated
  `feature/s3/manager`; `smbfs` uses the maintained `github.com/cloudsoda/go-smb2`
  fork (GO-2026-5051); dependencies are bumped (x/net, x/text, x/crypto,
  x/sys, AWS SDK, jlaffaye/ftp v0.2.4, Dropbox SDK v6.6.2).
- **Copies never take over setuid, setgid and sticky bits.** `CopyFile`,
  `CopyRecursive`, emulated moves, the overlay copy-up and
  `MemFileSystem.CopyFile` give the destination only the 9 permission bits of
  the source; pass `perm` to set special bits. A copy from a read-only file
  system (uploads, archive entries, `StdFileSystem`) gets the owner write bit,
  so an upload copies as 0644 instead of 0444. `NewFileInfo` (Local,
  `StdFileSystem`, sftpfs, smbfs) reports the special bits.
- **Directory links in recursive walks.** `ListDirRecursive` (native and
  emulated) never descends into a symbolic link to a directory, like
  `filepath.WalkDir`: the link itself is passed to the callback with
  `IsSymlink` true and `IsDir` false; a link at the start of the listing is
  followed. `CopyRecursive` and moves recreate a link to a directory as a
  link with its verbatim target within one file system instance that
  supports links. Between different instances, or if the destination can't
  create links, they fail before writing anything with an error wrapping
  `errors.ErrUnsupported`; links to files are copied as files. Copies never
  write through a symbolic link below the destination root, a link where a
  file or directory is to be written is an error wrapping `os.ErrExist`;
  this assumes nobody else changes the destination during the copy.
- **XAttr errors.** `XAttrCreate` on an existing attribute wraps
  `os.ErrExist`; `XAttrReplace` and `GetXAttr`/`RemoveXAttr` of a missing
  attribute wrap `os.ErrNotExist` (Local and MemFileSystem); fstest checks it.
- **Archive names.** `fs.Zip` and `ZipMemFiles` reject names that escape
  with `..`, names with a backslash and absolute names (the leading slash of
  a `MemFile` name is dropped) with an error wrapping `os.ErrInvalid`, so every archive
  they write unzips with `UnzipToMemFiles`; `zipfs.Reader` skips symbolic
  links, devices and FIFOs like `tarfs`.
- **Root directories.** `Remove` and `RemoveAll` of the root return an error
  wrapping `os.ErrInvalid` in every file system, also for relative paths of
  the root like `"."` in sftpfs and ftpfs, instead of deleting the whole
  bucket, container or server tree in s3fs, azureblobfs, sftpfs and ftpfs and
  calling `os.RemoveAll("/")` for the root of a local volume.
- **Paths.** Only a `~` at the start of the first part of a Local path means
  the home directory, a directory named `~` is written `./~`; a SubFileSystem
  stores relative link targets with `..` as paths of the parent; smbfs paths
  and link targets containing `\`, and link targets with a drive letter, are
  errors wrapping `os.ErrInvalid`; `TempFile` returns `InvalidFile` for an extension with a
  path separator; `StdFS` and SubFileSystems of Local apply the `os.DirFS`
  name rules (Windows device names, `\`, `:`, NUL) to the local file system
  only.
- **Same file under two names.** `LocalFileSystem.Move` renames a file onto
  its own directory entry under another spelling (case, Unicode
  normalization) and moves a file with more than one hard link through a
  temporary `.gofs-move-*` name, so it never removes the only link of a file;
  moves through bind mounts, symbolic links to a parent or a SubFileSystem
  onto the same file never copy and remove it, also through the local layers
  of an `OverlayFileSystem`. Moving a symbolic link onto the file it points
  to is an error wrapping `os.ErrInvalid`, like `mv` reports "are the same
  file", in Local, Mem, sftpfs, smbfs and the move emulation; `CopyFile` of a
  file onto itself through another path is a no-op. `fs.Move` of a directory
  to a name that only differs in case renames it.
- `ServeFileHTTP` passes seekable readers to `http.ServeContent` (local files
  use sendfile again), serves multi-range requests only from local files and
  in-memory readers (other readers answer with the whole content, so a
  request can't make a backend read the file once per range) and streams a
  file of unknown size.
- ftpfs and smbfs reject addresses with an unencoded `@` after the host like
  sftpfs; webdavfs rejects a base URL whose host has a port and whose path
  has a raw `@` without user info, or that has a password and an `@` after
  the host (write `%40`, or pass the credentials with `Options`). Error
  messages redact everything up to the last `@` of a URI with a password, so
  they can lose the host but never show a password.
- webdavfs follows no redirects to another host or port or from https to
  http, unless the `CheckRedirect` of `Options.Client` allows them, and then
  without credentials.
- sftpfs rejects addresses with an unencoded `@` after the host (`/`, `?` and
  `#` of the user info must be percent-encoded), drops a connection only after
  `KeepAliveCountMax` unanswered keep-alives and never for an SFTP status
  reply.
- `uuiddir.Enum` follows links to directories, multipartfs ignores form
  fields with an empty name, `OverlayFileSystem` documents that its layers
  must be case-sensitive.

### Added

- `fs.EnsureRegistered`, `fs.WriteAborter`, `fs.URIFileSystem`, `fs.XAttrFlags`,
  `File.Lstat`, `fs.NewStdDirEntry`, `fs.PermissionsFromFileMode`,
  `fs.UnzipToMemFiles` size cap (`UnzipToMemFilesMaxSize`), `fs.Event…`
  constants, `httpfs.New`/`NewAndRegister`, `smbfs.EnsureRegistered`,
  `dropboxfs.New`, `s3fs.New`, `NewMemFileSystemAndRegister`,
  `sftpfs.Options.HostKeyAlgorithms`, `sftpfs.KeepAliveCountMax`,
  `fsimpl.RedactURI`, `fsimpl.RedactAddress` and
  `fsimpl.RedactAddressWithoutScheme`.
- Streaming uploads in s3fs (multipart), azureblobfs (blocks) and dropboxfs
  (upload sessions, also above 150 MiB); multipart copies above 5 GiB in s3fs;
  native `MakeAllDirs` for the zip and tar writers.
- The root module builds on freebsd, netbsd, openbsd, solaris, illumos,
  dragonfly, aix, wasip1 and js/wasm.
- dropboxfs runs the conformance suite offline against an in-memory fake of
  the Dropbox API; sftpfs and ftpfs run it against in-process servers.
- CI: Go `stable` next to the go.work version, cross-compile jobs,
  `govulncheck`, `go mod tidy -diff` and `GOWORK=off` builds per module, lint
  on all three OSes, Dependabot; `tag-release.sh` tags all modules in lockstep
  and creates the GitHub release; `SECURITY.md`.

### Fixed

- Links created through a SubFileSystem escaped its root when one was created
  below another; names like `~/x` reached `$HOME` through `File(".").StdFS()`
  and SubFileSystems of Local; smbfs passed `..\` to the server, so
  SubFileSystems and CopyRecursive destinations on SMB were escaped.
- Passwords stayed in error messages for URIs that `url.Parse` rejects (a
  file named `100%.txt`) or misreads as `host:port`, and for passwords with
  `/`, `?` or `#` in sftpfs, ftpfs, smbfs and webdavfs addresses;
  httpfs transport errors kept presigned URL signatures; webdavfs redirects
  sent the basic auth and `Options.Header` to other hosts and over plain HTTP.
- `ServeFileHTTP` read a file that can't seek again for every range of a
  multi-range request, lost sendfile for local files and answered 500 for a
  file of unknown size.
- A failed copy onto an existing symbolic link deleted the link, and a link
  couldn't replace an existing file in the move emulation; Local `Move` onto a
  hard link of the source kept the source; ftpfs `Move` deleted the
  destination when the server refused the source; sftpfs (without
  posix-rename) and webdavfs moves deleted the source on a case-only rename
  or through an aliased path; dropboxfs `CopyFile` deleted the destination
  when the source had become a folder.
- Copying archive entries with setuid or setgid bits as root created setuid
  files; recursive copies followed directory link cycles until `ELOOP` and
  wrote about 40 nested partial copies; moving a file onto itself through
  bind mounts or a symbolic link to its directory deleted it.
- `EnsureRegistered` waiters got the creator's context cancellation; smbfs
  reconnect waiters dialed one after another after a dial timeout, a session
  dialed right before the context ended leaked, a connection broken during
  the checks of a Move wasn't retried; ftpfs leaked a connection when `Close`
  raced a running operation; sftpfs dropped the shared connection for status
  replies mentioning "connection reset" and killed slow downloads after one
  late keep-alive answer.
- `Close` contract: smbfs `io.Copy` on open files and `Remove` of the root,
  and sftpfs `Move`, answered without `ErrFileSystemClosed` after `Close`.
- azureblobfs `Touch` overwrote a blob created concurrently or hidden by an
  empty listing page, and `Remove` deleted the marker of a non-empty
  directory after an empty listing page; smbfs `CreateSymbolicLink` panicked
  for the targets `.` and `./`; dropboxfs returned a raw error for a source without
  write permission; sftpfs `ListDirRecursive` of a link to a directory
  returned only the link.
- zipfs listed symbolic links as regular files whose content was the target;
  `ZipMemFiles` wrote names that `UnzipToMemFiles` rejects; multipartfs
  overflowed the stack for a field with an empty name; `uuiddir.Enum` skipped
  linked level directories; the overlay returned Files with the volume of a
  layer; the XAttr errors of Local and MemFileSystem didn't match
  `os.ErrExist`/`os.ErrNotExist`; smbfs built invalid prefixes for `host:` and
  IPv6 zones.
- `ParseRawURI` allocated for every registered file system, `CopyFile` stat'ed
  the destination twice, `ReadAll` of httpfs, s3fs, azureblobfs and webdavfs
  grew the buffer instead of using the content length, the dropboxfs cache
  scanned all entries for every insert when full.
- Copy and Move reported success for a failed upload (the writer's Close
  error was dropped) and Move then deleted the source; a canceled or failed
  copy now aborts the upload and never removes the source.
- `Rename("")`, `Rename(".")` and `Rename("..")` deleted the file on file
  systems without native move; moving a directory into itself recursed until
  the path was too long; the RemoveAll emulation followed symlinks out of the
  tree; the ListDirRecursive emulation swallowed callback errors.
- webdavfs `Move`/`CopyFile` sent `Overwrite: T` and deleted an existing
  destination directory tree; `RemoveAll` did nothing on nginx; Apache
  redirects turned PROPFIND/DELETE/MOVE into GET.
- FTP command injection through CR/LF in paths; httpfs `Join("..")` could
  escape the host (SSRF); sftpfs and ftpfs registered credential URLs in
  `init`, so importing them made any URL dial out; `SubFileSystem` paths
  escaped the root through `..\`; `StdFS.Stat`/`Sub` accepted `..`; tar
  entries like `..\..\evil.txt` extracted outside the destination.
- The local watcher crashed the process when `Close` ran during a callback;
  sftpfs never closed the SSH connection and leaked one on every ctx timeout;
  a closed file system could stay registered forever after a race in
  `EnsureRegistered`; MemFileSystem panicked and deadlocked after `Close`.
- MemFileSystem shared byte slices with callers and open readers; writes to
  directories and through symlinks were stored on the wrong node; the
  overlay resurrected removed base contents and lost concurrent appends.
- sftpfs listings re-delivered entries and lost the callback error; ftpfs
  retried appends twice; non-idempotent operations are no longer retried after
  their request was sent; Dial honours ctx through the handshake.
- httpfs decoded and path-cleaned query strings (presigned URLs broke) and
  the extension of `report.pdf?x=1` was `.pdf?x=1`; RangeReader reported a
  truncated body as a complete file; tar and zip archives with duplicate
  entries returned corrupt content; `ContentHashFuncFrom` returned wrong
  hashes from the second call; Glob panicked when the loop was left early.
- s3fs copy sources with `+` failed; Touch wiped object metadata on s3fs and
  azureblobfs; keys like `d//x.txt` made a directory list itself; Dropbox
  uploads above 150 MB failed; the Dropbox metadata cache grew without bound.
- `Local` returned raw `EISDIR`/`ENOTDIR`/`ENOTEMPTY` errors, moved despite a
  canceled ctx and merged into existing directories on cross-device moves; a
  zero-value `LocalFileSystem` created mode 0000 files; `~foo` expanded to
  `$HOME/foo`.
- ftpfs: Append ran twice after a lost reply, ListDirRecursive returned
  partial results without an error, Dial ignored ctx during login, Close
  waited behind hung operations, data connections went to the IP of the PASV
  reply, FTPS servers that require TLS session reuse rejected transfers,
  `DebugOut` logged the password, IPv6 addresses without a port failed.
- smbfs: `Options.Password` was ignored for a username from the address,
  sessions dropped by the server were never reconnected, ReadAt at the end
  didn't return `io.EOF`, Move replaced empty directories, symlink targets
  lost their leading `/`; anonymous guest shares work.
- The root module did not compile on freebsd, netbsd, openbsd, solaris and
  wasm; `./test-workspace.sh` failed on Go 1.27 because of the pinned
  staticcheck version; the azureblobfs and smbfs dependencies had known
  vulnerabilities.

### Removed

- `fs.Remove(uris ...string)` and `fs.RemoveFile` (use `RemoveFiles` or the
  method expression `File.Remove`), `fs.MustGlob`, `File.MustGlob`,
  `fs.NewSingleMemFileSystem`, `MemFileSystem.WithID`/`WithVolume`,
  `fs.WriteSeekCloser`, the `Permissions.CanXxx` methods (use `Can`),
  `fs.RenameFileSystem`, `fs.ListDirMaxFileSystem`,
  `fsimpl.NewFileBufferWithClose`, `fsimpl.DirTreeNode` (now
  `internal/dirtree`), `fstest.DockerAvailable`/`DockerSetupFailed`.

## [1.0.0-beta.1] - 2026-10-20

First beta of the v1 API; further beta releases iterate on it before
v1.0.0 freezes the API. Upgrading from v0.x is mechanical, see
`docs/MIGRATION_v1.md`; the design decisions are recorded in
`docs/V1_ROADMAP.md`. All modules (`s3fs`, `sftpfs`, `ftpfs`, `dropboxfs`,
`webdavfs`, `smbfs`, `azureblobfs`) are tagged in lockstep; the `tools`
module only pins development tools and is not tagged.

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
  `ParseRawURI` decodes URL escapes only for URIs with a scheme, and only in
  the path after the prefix, so a local file literally named `a%20b` is
  reachable. The URI of a path escapes every `%` as `%25`, see Fixed.
- **Backends implement only the optional interfaces they do better than the
  generic emulation.** `dispatch.go` already emulates `Exists`, `ReadAll`,
  `Touch`, `Append`, `OpenAppendWriter`, `OpenReadWriter` and `Truncate` on top
  of the core primitives, so a backend that had no native advantage now drops
  its implementation rather than restating the emulation. Behaviour is
  unchanged for callers, who reach these through the `File` API either way.
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
  without extended attributes, `zipfs.NewWriter` closes the underlying
  file on `Close` (the handle used to leak), and `fs.Glob` yields files
  cleaned for their file system's separator.

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
- The 23 `fs.Permissions` values (`UserRead`, `AllReadWrite`, ...) are `const`
  instead of `var`, so they can't be reassigned by a consumer and can be used
  in constant expressions.
- `zipfs.Reader.ID()` returns the prefix of the archive (`zip://<id>`) like
  `zipfs.Writer`, `tarfs` and `multipartfs`, instead of the prefix without
  the scheme that it inherited from the embedded `fs.StdFileSystem`.
- The s3fs tests run against Adobe's S3Mock (`adobe/s3mock:5.2.3`) instead
  of MinIO, whose Docker images were removed.

### Added

- A README for every module and every sub package that implements a
  `FileSystem`: `sftpfs`, `ftpfs`, `webdavfs`, `smbfs`, `azureblobfs`,
  `tools`, `httpfs`, `zipfs`, `tarfs` and `multipartfs`, next to the
  existing ones for `s3fs` and `dropboxfs`.
- Runnable `Example` functions for the `fs`, `httpfs`, `zipfs`, `tarfs`,
  `multipartfs` and `uuiddir` packages, and compile-only examples for the
  constructors of the seven backend modules, so the documented usage is
  verified by `go test` and shown on pkg.go.dev.
- A doc comment on every exported symbol of every package.
- `fs.StdFileSystem` adapts any `io/fs.FS` (`embed.FS`, `os.DirFS`,
  `zip.Reader`, `testing/fstest.MapFS`) as a read-only file system with the
  prefix `stdfs://<id>`; the counterpart of `StdFS`.
- `fs.SubFileSystem` is a view of a directory of another file system with
  the prefix `sub://<id>`, forwarding every operation (including the
  optional interfaces) to the parent with translated paths.
- `smbfs` module: an SMB2/3 client file system on the pure Go go-smb2,
  with native append and read-write handles, `Truncate`, `Touch`,
  `MakeAllDirs`, `RemoveAll`, server-side `Move`, `SetPermissions`
  (read-only attribute) and symbolic links where the server allows them;
  tested against a Samba container.
- `azureblobfs` module: Azure Blob Storage with marker-blob directories,
  seeking reads via range requests, server-side `CopyFile` and `Touch` via
  metadata; tested against the Azurite emulator.
- `fstest.Config.PermissionMask` declares which permission bits a file
  system stores, so the suite checks `SetPermissions` for those bits only.
- `webdavfs` module: a WebDAV client file system with the standard library
  only (`PROPFIND` for `Stat` and `ListDir`, `PUT`, `MKCOL`, `DELETE`,
  native `MOVE` and `COPY`, seeking reads with `Range` requests), tested
  against an in-process `golang.org/x/net/webdav` server.
- `fs.OverlayFileSystem` stacks a writable upper layer on a read-only base
  with the prefix `overlay://<id>`: reads fall through, listings are the
  union, writes go to the upper layer with copy-up for in-place changes,
  removed base entries are hidden by in-memory whiteouts.
- `fs.NewStdFileSystemWithPrefix` builds a `StdFileSystem` with a scheme of
  its own; zipfs uses it: `zipfs.Reader` is a `StdFileSystem` over
  `archive/zip.Reader` and `zipfs.Writer` the sequential writer,
  replacing the mode-switching `ZipFileSystem` type. Reader listings are
  sorted by name and report the modes stored in the archive.
- `tarfs`: read-only and write-only file systems for tar archives,
  optionally gzip compressed, mirroring `zipfs` with separate `tarfs.Reader`
  and `tarfs.Writer` types (root module, standard library only).
  `fsimpl.DirTree` is the directory tree of archive entries (formerly
  internal to zipfs).
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
- `fsimpl.EscapeURIPath` and `fsimpl.UnescapeURIPath` convert between a file
  system path and the path part of its URI, for implementations that build
  URIs without `PathHelper.URL`.
- `fsimpl.NewWriteOnCloseFileBuffer` for file systems that upload whole files
  on Close; `fsimpl.FileBuffer.Truncate`.
- **`fsimpl.RangeReader`** implements `io.ReadSeekCloser` and `io.ReaderAt`
  over a backend that serves byte ranges; webdavfs and azureblobfs share it
  instead of each carrying a private copy.
- `multipartfs.New` wraps a `*multipart.Form` that was parsed by the caller
  (`FromRequestForm` uses it); `MultipartFileSystem.FormValue` and
  `FormValues` read the non file form fields.

### Fixed

- **File names that look URL escaped keep their name on every file system.**
  `ParseRawURI` decodes the URL escapes of a URI with a scheme, but the URIs
  built from paths (`File.Join`, `File.Dir`, `File.URL`, the `FileInfo.File`
  of listings, `PathHelper.URL` and `JoinCleanURI`, `LocalFileSystem.URL`)
  did not escape them: `memFS.RootDir().Join("a%20b.txt")` created
  `a b.txt`, and an S3 key `100%25.txt` was read as `100%.txt`. The URI of a
  path now escapes every `%` as `%25`, `ParseRawURI` decodes only the path
  after the prefix (so a prefix with escapes, like an smbfs user name,
  resolves too), `File.Ext` is taken from the decoded path, `SubFileSystem`
  and `OverlayFileSystem` decode the files of their layers, and webdavfs no
  longer decodes PROPFIND hrefs twice. The conformance suite seeds a file
  named `100%25 a%20b.txt` on every backend.
- s3fs multipart uploads (`WriteAll` from `MultipartUploadThreshold` on)
  use the `RequestChecksumCalculation` of the client: the SDK uploader always
  sent CRC32 part checksums, so a client configured with `WhenRequired` for
  a service without checksum support could not complete them. `Touch` of an
  existing object returns an `fs.ErrUnsupported` error when the service
  refuses to copy the object onto itself.
- **multipartfs files open their own upload for every file and field name.**
  A file `..%2Fother%2Fx.txt` uploaded under the field `doc` read the upload
  `x.txt` of the field `other`, and files named like `report%201.pdf`,
  `100%25.txt` or `a%2Fb.txt` did not exist. File names now keep their
  name, and the directory name of a field is escaped (`%` as `%25`, `/` as
  `%2F`, `.` and `..` as `%2E` and `%2E%2E`) and unescaped after the path is
  split, so field names like `a/b` or `..` work and no name can address an
  upload of another field.
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
- multipartfs: real sizes instead of `-1`, prefixed `FileInfo.File`,
  `ErrDoesNotExist`/`ErrIsNotDirectory` from listing, idempotent `Close`.
  Every method reports `ErrFileSystemClosed` after `Close` instead of serving
  the parts that are still in memory; the root directory exists and can be
  stat-ed; `ListDir` applies the patterns to the form field directories too;
  files uploaded under an already used name (or under a name that is not a
  usable path element like `..`) get a unique file system name instead of
  shadowing each other. Uploaded files carry no modification time, so
  `FileInfo.Modified` is the zero time.
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
- `fs.MemFileSystem.ReadAll` on a directory returns `ErrIsDirectory` instead
  of empty data.

## [0.1.0] - 2026-06-30

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
