# go-fs v1.0 roadmap

Plan of record for reaching v1.0.0: clean, durable abstractions and a complete
feature surface for the areas the library touches, so the API can be frozen.
Breaking changes are allowed until v1.0.0 is tagged.

This document keeps the decisions that freeze with v1.0.0 and the work that is
left. What changed in each release is in [CHANGELOG.md](../CHANGELOG.md), the
way from v0.1.0 in [MIGRATION_v1.md](MIGRATION_v1.md), and the API in the
[README](../README.md) and the [docs](README.md).

## Status

| Release        | Date       | Pull request                     |
| -------------- | ---------- | -------------------------------- |
| v1.0.0-beta.1  | 2026-10-20 | #18, #21 (v1.0 phases), #24      |
| v1.0.0-beta.2  | 2026-10-23 | #25 (full review of beta.1)      |
| v1.0.0-beta.3  | 2026-10-24 | #27                              |
| v1.0.0         | open       | see [Remaining for v1.0.0](#remaining-for-v100) |

## Decisions

These hold for v1.0.0. Where an earlier design differs, the decision here wins.

### API shape

- **Context rule.** A method takes `ctx context.Context` as its first
  parameter only if it can take long: content transfer, listings, recursive
  removal, moves and copies (also `Rename`, `MoveFileSystem.Move` and
  `TruncateFileSystem.Truncate`, which may copy), and dialing. Paths,
  metadata, open, single-entry operations, ownership, links, xattrs and
  watches are ctx-less, as are the standard library interfaces (`WriteTo`,
  `ReadFrom`, `String`). There are no `*Context` twins. The full table is in
  [explanation-context-rule.md](explanation-context-rule.md).
- **Implementer interface.** `FileSystem` holds the primitives only (`ID`,
  `Prefix`, `Name`, `String`, `Separator`, `ReadableWritable`, `RootDir`,
  `CleanPath`, `Stat`, `ListDir`, `OpenReader`, `Close`), `WriteFileSystem`
  adds `OpenWriter`, `MakeDir` and `Remove`. Everything else is an optional
  interface, emulated generically in `dispatch.go` where a fallback exists.
  A backend implements an optional interface only when it does the operation
  better than the emulation, never to return `ErrUnsupported`. The read-only
  and write-only gates live in the `fs` package. See
  [reference-filesystem-interfaces.md](reference-filesystem-interfaces.md).
- **`FileSystem.ID()`** is a string that identifies the file system uniquely
  among the registered ones, computed at construction and never blocking: the
  backing store's own id where it has one (local: `statfs` `f_fsid`, `statvfs`
  on NetBSD, Solaris and illumos, Windows volume serial; dropbox: the account
  id), otherwise the coordinates of the store (bucket, `host/container`,
  `user@host/share`, ...). The values are tabulated in the README.
- **Constructors never register implicitly.** `New…`/`Dial…` return the file
  system (and an error where construction can fail); `…AndRegister` registers
  and returns only an error. Every constructor whose registration behaviour
  changed also changed its signature, so old call sites fail to compile.
- **Atomic registry.** Prefixes match at a boundary, the longest match over
  prefixes and aliases wins, `Register` returns `ErrAlreadyRegistered` for
  another instance and is not reference counted, `fs.EnsureRegistered` is the
  atomic, reference counted get-or-create, `Close` always unregisters.
- **Per-instance `*Options`** in every backend, no mutable package
  configuration; permission defaults are constants.
- **Writers.** Writers that commit on `Close` implement `WriteAborter`; the
  generic emulations commit only complete data.
- `CopyFileBuf` takes a plain `buf []byte`. `IsSymbolicLink` stays on `File`
  and `FileReader`; implementers provide `Lstat`.
- Dropped: `MemDir` (`MemFile` keeps its trailing-slash directory semantics),
  `File.GobEncode`/`GobDecode`, `ListDirChan`, public `FileInfoCache`,
  `ReadOnlyBase`, `FullyFeaturedFileSystem`.
- **`fsimpl` stays public** (decided again 2026-10-25). An `internal`
  package would compile in the backend modules, because the internal rule
  is path based, but it would buy no freedom: the backends require the root
  module at the placeholder pseudo-version, so `s3fs v1.0.0` can be built
  against any later `go-fs v1.x`, and every helper they use has to stay
  compatible anyway. Third-party backends need `PathHelper` and
  `EscapeURIPath` to pass the conformance suite, and downstream code uses
  `Ext`, `TrimExt`, `RandomString` and `DropboxContentHash`. Its whole API
  stays, also `ReadWriteAllSeekCloser`, which only the root module uses.
- **Content hash** (2026-10-25). `DefaultContentHash` stays the Dropbox
  content hash algorithm, always computed from the content, with no optional
  interface for hashes from metadata. Of all backends only Dropbox stores this
  algorithm (`FileMetadata.ContentHash`); S3 (ETag, CRC64NVME and other
  checksums), Azure (Content-MD5 only for single-request uploads) and zip
  (CRC-32) store other algorithms, and only under conditions. An optional
  interface tagged with the algorithm can be added in a v1.x release without
  breaking anything.

### Semantics

- **Paths.** A file system path is the output of `CleanPath`: cleaned, with
  `Separator()`, without the prefix, absolute for the file system (httpfs is
  the one unrooted file system). Backends never see the prefix.
- **URIs.** The URI of a path escapes every `%` as `%25`
  (`fsimpl.EscapeURIPath`); `ParseRawURI` decodes percent-encoded characters only for URIs
  with a scheme and only after the prefix, so every name survives the round
  trip. Default-port aliases resolve via `PrefixAliasFileSystem`.
- **URI, not URL** (2026-10-25). The string of a `File` and of
  `File.URI()` (`File.URL()` before) is a go-fs URI: the prefix plus the path
  with only `%` percent-encoded, so it is no RFC 3986 URL. "URL" is used only
  for real HTTP(S) URLs (httpfs, the webdavfs base URL, the Azure container
  URL). A conversion to a `*url.URL` can be added in v1.x.
- **File URIs** follow RFC 8089 (2026-10-25): `file:///etc/hosts`,
  `file:///C:/dir` for a Windows drive and `file://server/share/x` for a UNC
  path. Parsing also accepts `file://localhost/…` and, on Windows, the
  `file://C:/…` form of the betas. A file URI with another host is a UNC path
  on Windows and the `Invalid` file system elsewhere, never a path relative
  to the working directory. `file://~/x` is the home directory like `~/x`.
- **Error contract.** `errors.Is` holds for `os.ErrNotExist`, `os.ErrExist`,
  `os.ErrPermission` (where the backend can tell), `ErrReadOnlyFileSystem`,
  `ErrWriteOnlyFileSystem`, `ErrFileSystemClosed`, `ErrIsDirectory`,
  `ErrIsNotDirectory`, `ErrDirectoryNotEmpty`, `ErrFileSystemsDoNotMatch`,
  `ErrAlreadyRegistered` and `ctx.Err()`. Typed errors store the `FileReader`
  behind a pointer, so `errors.Is` never panics for non-comparable readers.
  `ErrEmptyPath` does not wrap `os.ErrNotExist`; unregistered prefixes report
  `ErrInvalidFileSystem`. See [reference-errors.md](reference-errors.md).
- **Overwrite.** Move, Rename and CopyFile replace a file destination; a
  directory destination is an error, never merged or deleted. Moving into
  itself and removing the root are `os.ErrInvalid` on every file system.
- **Links.** Recursive walks never descend into directory links
  (`filepath.WalkDir` semantics). Links to directories are recreated only
  within one file system instance; between instances `CopyRecursive` and
  `Move` fail with `errors.ErrUnsupported`. Moving a link onto its target is
  `os.ErrInvalid`.
- **Copies** keep only the 9 permission bits of the source unless `perm` is
  passed (no setuid from archives).
- Overlay layers must be case-sensitive. XAttr flag errors wrap
  `os.ErrExist`/`os.ErrNotExist`.
- **Empty indent** (beta.3). `WriteJSONIndent`/`WriteXMLIndent` put every
  element on its own line without indentation when the indent is empty; only
  `WriteJSON`/`WriteXML` write compact output. XML uses a NUL line prefix
  that is removed only directly before a `<`.
- **Backslashes in names** (2026-10-25). sftpfs, ftpfs and webdavfs reject
  a path with `\` (and sftpfs a link target) with `os.ErrInvalid`, like
  smbfs: their separator is `/`, but a server on Windows takes `\` for a
  separator, so `..\x` would leave the directory it is meant to be in.
  `Options.AllowBackslashInNames` allows it for servers on Unix that store
  backslashes in names. Only the paths passed to a file system are checked,
  names a server lists are passed through.
- **Azure snapshots** (2026-10-25). Azure deletes a blob with snapshots only
  if the request says what to do with them. `azureblobfs` refuses by default
  (`ErrSnapshotsPresent`; `RemoveAll` checks the whole tree before it deletes
  anything) and deletes the snapshots with the blob only with
  `Options.DeleteSnapshots`, like rclone's `delete_snapshots`.

### Project rules

- Standard library errors only, no `go-errs` (see `AGENTS.md`).
- Go version policy: every module and `go.work` declares one minor version
  behind the current Go release, bumped together.
- All modules are tagged in lockstep with `tag-release.sh`; the `tools`
  module is never tagged.
- `docs/MIGRATION_v1.md` covers only the way from v0.1.0 to the latest
  release, no beta-to-beta sections.
- Unit tests run offline; Docker-backed tests skip without Docker; tests
  that need credentials or public servers are gated behind
  `GOFS_ONLINE_TESTS=1` or a token variable.

## Remaining for v1.0.0

### Fixed before the freeze (2026-10-25)

- [x] File URIs follow RFC 8089 (`file:///C:/…`, UNC, `localhost`, other
      hosts no longer cwd-relative).
- [x] azureblobfs: blobs with snapshots (see the decision above); range reads
      of `OpenReader` pinned to the ETag.
- [x] ftpfs: link entries are no longer reported as regular files; `Stat`
      follows links, `Remove`, `RemoveAll` and `Move` act on the link.
- [x] s3fs: `Stat`, the directory check before writes and the not-empty check
      of `Remove` follow truncated short `ListObjectsV2` pages.
- [x] The docs no longer claim that dropboxfs answers `ContentHash` from
      metadata.
- [x] sftpfs, ftpfs and webdavfs reject backslashes in paths unless
      `Options.AllowBackslashInNames` is set (see the decision above).
- [x] `File.URL()` → `File.URI()`, `FileURLs` → `FileURIs`,
      `PathHelper.URL` and `LocalFileSystem.URL` → `URI` (see the decision
      above).
- [x] `fs.ReadHeader`/`ReadHeaderString` take a ctx (content transfer);
      `fsimpl.ReadonlyFileBuffer` → `ReadOnlyFileBuffer`;
      `fs.NewFileReadWriteAllSeekCloser` stays and got a test.
- [x] Pre-release audit: doc drift (Docker skip rules, conformance how-to,
      Dropbox prefix vs id, buffering of `OpenReadWriter`, tutorial,
      `FileInfo` field docs) and hygiene (`.gitignore`, `#nosec` reasons,
      cross-compile targets).

### Verification

- [ ] Migrate go-docdb and domonda-service against the latest beta. The last
      smoke compile was in Phase 4, before beta.2 changed constructors,
      registry and errors.
- [ ] azureblobfs against real Azure: x-ms-version 2026-12-06 is unverified
      (Azurite 3.37 rejects it without `--skipApiVersionCheck`), snapshots and
      ETag-pinned reads included.
- [ ] dropboxfs with a real `DROPBOX_ACCESS_TOKEN`.
- [ ] Everything in [Verification](#verification) below.

### Release

- [ ] CHANGELOG `[1.0.0]`, `VERSION` = `v1.0.0`, the MIGRATION intro names
      v1.0.0 instead of the betas.
- [ ] Tag all modules with `tag-release.sh` after the merge.
- [ ] Update the dependents of go-fs (tracked outside this repository).

## Not in v1.0.0

Additive or internal, so they can land in v1.x releases:

- Performance: `sftp.UseConcurrentWrites` in sftpfs (uploads are capped at
  32 KiB per round trip); the Azure blob batch API for `RemoveAll` (one
  DELETE per blob today); `fsimpl.RangeReader` reopens the body on every
  seek instead of skipping short forward gaps; ftpfs `ListDirRecursive`
  collects the whole tree before the first callback.
- s3fs and azureblobfs send a listing request before every write to check
  that the destination is no directory; an `Options` flag to skip it.
- s3fs `ListDir`, `ListDirRecursive` and `RemoveAll` trust the SDK paginator,
  which ends silently at a truncated page without a continuation token.
- An algorithm-tagged optional interface for content hashes from metadata
  (Dropbox `content_hash`, S3 checksums).
- ftpfs detects links only on servers without MLST: with MLSD/MLST,
  `jlaffaye/ftp` reads `type=OS.unix=slink:…` as a file and drops the target,
  and servers like ProFTPD report the facts of the target. Fixing it needs
  raw MLSD access, which the client library doesn't offer.
- smbfs link behaviour is not covered by the conformance link check (the test
  Samba server rejects links); uuiddir walks a link to a directory like a
  directory.
- Copies check the destination with `Lstat` and then open it by path; a real
  fix for Local needs `os.Root` or `O_NOFOLLOW` based writes.
- `fsimpl.RedactURI` edge cases (`http://localhost:8080/me@example.com`, a
  password with `/` and `@`); webdavfs rejects base URLs with a port and a
  raw `@` in the path.
- azureblobfs: pin the listing `ResponseFormat` to XML.

## Verification

- `./test-workspace.sh` (with `-race`) green locally; GitHub Actions green on
  ubuntu, macos and windows for every module.
- `fstest.RunConformance` passes on Local, Mem (both separators and with a
  volume), Sub, Overlay, Std, httpfs (httptest), zipfs and tarfs reader and
  writer, multipartfs, s3fs (S3Mock), azureblobfs (Azurite), sftpfs
  (in-process and Docker sshd), ftpfs (in-process FTP and FTPS server), smbfs
  (Docker Samba), webdavfs (x/net/webdav with nginx and Apache behaviour) and
  dropboxfs (offline fake and token).
- The root module builds for linux, darwin, windows, the BSDs, solaris,
  illumos, aix, wasip1 and js/wasm.
- `go vet`, staticcheck, gosec and govulncheck clean, `go mod tidy -diff`
  clean for every module.
- `grep -rn 'TODO\|FIXME' --include='*.go'` returns nothing unaddressed; no
  commented-out code blocks remain.

## History

- **Phases 0–6** (2026-09, PR #18, released as v1.0.0-beta.1): CI on three
  operating systems, the conformance suite `fstest.RunConformance`,
  `fsimpl.PathHelper`, the primitives-only `FileSystem` interface with the
  generic emulations in `dispatch.go`, the ctx rule on `File`, `FileReader`
  and `MemFile`, the reworked s3fs, sftpfs, ftpfs and dropboxfs, and the
  docs. The breaking changes were bounded by a downstream corpus of 1536 Go
  files (mainly domonda-service): `fs.FileReader`, `fs.File` and `MemFile`
  dominate, nobody outside this repository implements `FileSystem`.
- **Beta iterations** (same PR): `StdFileSystem`, `SubFileSystem`,
  `OverlayFileSystem`, `tarfs`, the `Reader`/`Writer` split of zipfs and
  tarfs, the backend modules webdavfs, smbfs and azureblobfs, URI `%`
  escaping (#24).
- **Beta 2** (#25): the decisions on constructors, the registry, options,
  writers, errors and overwrite semantics above, after a full review of
  beta.1 that found data loss, security and crash bugs; a pre-release review
  of confinement escapes, password leaks and link handling.
- **Beta 3** (#27): empty-indent newlines, inlined marshal helpers, backend
  dependency bumps, azureblobfs fixes for unexpected server answers.
