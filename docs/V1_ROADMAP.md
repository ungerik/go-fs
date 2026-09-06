# go-fs v1.0 roadmap

Plan of record for reaching v1.0.0: clean, durable abstractions and a complete
feature surface for the areas the library touches, so the API can be frozen.
It replaces the earlier `TODOS.md`; every still-open item from it is folded into
the phases below. Breaking changes are explicitly allowed before v1.0.0.

Two audits of the whole tree (core package, all backends, test infrastructure)
plus a design pass produced the findings and decisions recorded here.

## Decisions

Taken 2026-09-05/06:

- **Context arguments.** The `*Context` twins are dropped. A method takes
  `ctx context.Context` as its first parameter only if it is a potentially
  long-running read or write operation:
  - content transfer: `ReadAll*`, `WriteAll*`, `Append*`, `ContentHash`,
    `ReadJSON`/`WriteJSON`/`ReadXML`/`WriteXML`, `CopyFile`, `CopyRecursive`,
    `Move`/`MoveTo`, `Truncate`;
  - directory iteration: `ListDir*`, `Glob`, `RemoveRecursive`,
    `RemoveDirContents*`, `RemoveAll`;
  - long-running connect/constructor functions: `sftpfs.Dial*` /
    `EnsureRegistered`, `ftpfs.Dial*` / `EnsureRegistered`,
    `s3fs.NewLoadDefaultConfig`, `dropboxfs.New*` (fetches the account id).

  Metadata and open operations stay ctx-less (`Stat`, `Info`, `Exists`, `IsDir`,
  `Size`, `Open*`, `MakeDir`, `MakeAllDirs`, `Remove`, `Touch`, `Rename`,
  permissions/owner/symlink/xattr), as do pure path methods and methods that
  implement standard library interfaces (`WriteTo`, `ReadFrom`, `GobEncode`,
  `String`). Where a pair exists today, the ctx variant survives under the
  short name.
- **`FileSystem.ID()` stays** as `ID() string`: a stable identifier of the
  backing store, unique among registered file systems, computed at
  construction, never blocking. Local: the real underlying file system id of the root volume
  (`statfs` `f_fsid` on Unix, `GetVolumeInformation` volume serial on Windows),
  obtained lazily once via `sync.Once`; s3: bucket;
  sftp/ftp: `user@host`; mem: generated id; dropbox: account id fetched by the
  constructor (which takes ctx and may fail). Useful as meta information, e.g.
  `fs.File("/path").FileSystem().ID()`.
- **Errors:** standard library `errors`/`fmt` only, no `go-errs` dependency
  (see `CLAUDE.md`).
- **`MemDir` is dropped**; `MemFile` keeps its trailing-slash directory
  semantics.
- **Delivery:** this roadmap lives in the repo; all of the v1.0 work lands
  in one pull request (#18, the v1.0 PR), one commit per phase (backends:
  one commit each), so master jumps from v0.1.0 to v1.0.0 in one step.
- **Go version policy:** every module (and `go.work`) declares one minor
  version behind the currently released Go: Go 1.27 is current, so all modules
  are on `go 1.26.0`. Bump all modules together on each Go release.

## What bounds the breakage

Downstream corpus: 1536 Go files under `/Users/erik/go/src/github.com`
(mainly domonda-service).

| Identifier                            | Uses  |
| ------------------------------------- | ----- |
| `fs.FileReader`                       | 3201  |
| `fs.File`                             | 2545  |
| `fs.MemFile` / `NewMemFile`           | 1581  |
| `ReadMemFile` / `ReadMemFileRename`   | 161   |
| `CopyFile`, `TempDir`, `MakeTempDir`  | ~170  |
| `FileInfo`                            | 36    |
| everything else in the root package   | < 30 each, most 0 |

Nobody outside this repo implements `FileSystem`; only the in-repo backends
and the `fstest` mocks must be migrated when the interface changes. Hot
`File` methods: `Join`, `Name`, `Info`, `Exists`, `ReadAll`, `LocalPath`,
`WriteAll`, `AbsPath`, `Ext`, `IsDir`, `Remove`, `ReadAllContext`, `Path`,
`Size`, `Dir`, `RemoveRecursive`, `Joinf`. Unused downstream: `ListDirChan`,
`MemDir`, `StdFS`, `SortBy*`, `Glob`, `FileInfoCache`, `Watch` (1 use).

## Target architecture

### Core `FileSystem` interface (implementer-facing)

```go
type FileSystem interface {
	ID() string           // stable backing-store id, computed at construction, never blocks
	Prefix() string       // URI prefix; Prefix()+path == URI
	Name() string
	String() string
	Separator() string
	ReadableWritable() (readable, writable bool) // dynamic (mem SetReadOnly, s3 readOnly, zip mode)
	RootDir() File        // may be InvalidFile (httpfs)
	CleanPath(uriParts ...string) string // pure, never mutates input, strips Prefix() from uriParts[0]

	Stat(path string) (*FileInfo, error)
	ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error
	OpenReader(path string) (io.ReadCloser, error)
	Close() error // idempotent; unregisters if registered; no-op for Local/Invalid
}

type WriteFileSystem interface {
	FileSystem
	OpenWriter(path string, perm Permissions) (io.WriteCloser, error) // create|truncate
	MakeDir(dirPath string, perm Permissions) error                   // os.ErrExist if exists
	Remove(path string) error                                         // os.ErrNotExist if missing; non-empty dir is an error
}
```

Removed from the interface: `URL`, `CleanPathFromURI`, `JoinCleanFile`,
`JoinCleanPath`, `SplitPath`, `SplitDirAndName`, `MatchAnyPattern`,
`IsAbsPath`/`AbsPath` (optional, local only), `IsHidden`, `IsSymbolicLink`
(become `FileInfo` fields), `OpenReadWriter` (optional),
`FullyFeaturedFileSystem`, `ReadOnlyBase`.

`FileInfo` gains `IsSymlink bool` and `Sys any`; the `fs` package fills
`File`, `Name` and `IsHidden` (dot rule) when a backend leaves them empty.

Optional interfaces, all embedding `FileSystem`:

- ctx-taking (content transfer, iteration): `ReadAllFileSystem`
  (`ReadAll(ctx, path)`), `WriteAllFileSystem` (`WriteAll(ctx, path, data,
  perm)`), `AppendFileSystem`, `CopyFileSystem` (`CopyFile(ctx, src, dest)`;
  the `buf *[]byte` parameter is dropped), `RemoveAllFileSystem`
  (`RemoveAll(ctx, path)`; new: `os.RemoveAll`, S3 batch delete, Dropbox
  recursive delete), `ListDirMaxFileSystem`, `ListDirRecursiveFileSystem`.
- ctx-less: `ExistsFileSystem` (`Exists(path) (bool, error)`),
  `AppendWriterFileSystem`, `ReadWriterFileSystem` (`OpenReadWriter`),
  `TruncateFileSystem`, `TouchFileSystem`, `MakeAllDirsFileSystem`,
  `MoveFileSystem` (`Move(src, dest)`, same-FS rename semantics, dest is
  always the final path; `File.MoveTo(ctx, ...)` handles "into directory" and
  the cross-FS copy fallback), `RenameFileSystem`, `WatchFileSystem` (the
  cancel func is the lifetime), `PermissionsFileSystem`, `UserFileSystem`,
  `GroupFileSystem`, `SymbolicLinkFileSystem`, `XAttrFileSystem`,
  `VolumeNameFileSystem`, `AbsPathFileSystem` (`IsAbsPath`, `AbsPath`,
  `RelPath`; merges `RelPathFileSystem`).

The `fs` package emulates an optional interface generically where a
primitive-based fallback exists (Exists via Stat, ReadAll via OpenReader,
WriteAll via OpenWriter, Append via ReadAll+WriteAll, Truncate, MakeAllDirs,
RemoveAll, CopyFile, Move via copy+delete, ListDirMax, ListDirRecursive) and
returns `ErrUnsupported` otherwise. A backend implements an optional interface
only when it is more efficient or more capable than the emulation, never just
to return `ErrUnsupported` (the s3fs `Watch` stub goes away).

### Contracts enforced by the conformance suite

- **Paths.** A file-system path is the output of `CleanPath`: cleaned, uses
  `Separator()`, carries no prefix, and is absolute for that file system
  (rooted at the separator or a volume; httpfs is the one unrooted file system
  where the path starts with the host). The `fs` package guarantees that
  backends never see the prefix (`ParseRawURI` calls `CleanPath`). URL
  unescaping happens only in `ParseRawURI` for inputs that carried a scheme,
  never in `CleanPath`, so a local file literally named `a%20b` is reachable.
  Default-port aliases (`sftp://u@h:22`, `ftp://h:21`, `ftps://h:990`) resolve
  via `PathHelper.AltPrefixes` and registry matching on all prefixes.
- **Errors.** `errors.Is` must hold for `os.ErrNotExist`
  (Stat/OpenReader/Remove/ListDir), `os.ErrExist` (MakeDir), `os.ErrPermission`
  where the backend can tell (optional per backend, FTP 550 is ambiguous),
  `ErrReadOnlyFileSystem`/`ErrWriteOnlyFileSystem` (pre-gated in the `fs`
  package via `ReadableWritable()` so backends need no stubs),
  `ErrFileSystemClosed` after `Close`, `ErrIsDirectory`/`ErrIsNotDirectory`,
  and `ctx.Err()` on cancellation. A typed `ErrFileSystemsDoNotMatch` is added
  for cross-FS Move/Copy/Rename/RelPath/Symlink.
- **Permissions.** A single `Permissions` value on the interface; `0` means
  "file system default". `File` methods keep `perm ...Permissions` (OR-joined,
  none = default).
- **Touch.** The fallback creates only when missing; on an existing file
  without `TouchFileSystem` it returns `ErrUnsupported` and never truncates.
- **Remove / MakeDir.** `Remove` of a missing path is `ErrNotExist`; of a
  non-empty directory an error. `MakeDir` on an existing path is `ErrExist`;
  implicit-directory file systems (s3, zip writer) may return nil.
- **Close.** Idempotent; unregisters if registered; ref-counting stays a
  backend concern (`sftpfs`/`ftpfs` `EnsureRegistered`). Constructors do not
  register as a side effect except the documented `*AndRegister` variants;
  `NewMemFileSystem` gets a separate `Register` step (or
  `NewMemFileSystemAndRegister`).
- **Readers.** Backends return a plain `io.ReadCloser` and stream where the
  transport allows (S3 `GetObject` body, HTTP body, Dropbox download).
  `File.OpenReader` still returns `fs.ReadCloser` (= `iofs.File`): passed
  through when the backend reader already implements it, otherwise wrapped
  with a lazy `Stat`.

### `fsimpl` (implementer helpers)

- Add `fsimpl.PathHelper{Prefix, Separator string; Rooted bool; AltPrefixes
  []string; VolumeLen func(string) int}` with `CleanPath`, `File(path)`,
  `SplitPath`, `SplitDirAndName`, `URL`, `PathFromURI`, `IsHidden`;
  separator-aware (convert to `/`, `path.Clean`, convert back) so the mem `\`
  mode is correct. Backends embed it and drop their copy-pasted path methods.
- Add `NewWriteAllOnCloseBuffer(seed []byte, write func([]byte) error)`
  replacing the seven self-referential `FileBuffer` closures, and a shared
  closed-state guard.
- Fix `FileBuffer.WriteAt` (negative offset panic, `io.WriterAt` contract),
  add `FileBuffer.Truncate`, make `NewFileBuffer` set a `FileInfo`.
- Remove dead exports: `DirEntryFromFileInfo`, `NewReadonlyFileBufferWithClose`,
  `ReadWriteAllSeekCloser.InvalidateBuffer`.
- `fsimpl` stays public (downstream uses `RandomString`, `Ext`, `TrimExt` and
  the buffers).

### `File` / `FileReader` / `MemFile` (consumer-facing)

- Post-v1 `FileReader`: `String`, `Name`, `Ext`, `LocalPath`, `Size`, `Exists`,
  `CheckExists`, `IsDir`, `CheckIsDir`, `OpenReader`, `OpenReadSeeker` (no
  ctx); `ContentHash(ctx)`, `ReadAll(ctx)`, `ReadAllContentHash(ctx)`,
  `ReadAllString(ctx)`, `ReadJSON(ctx, v)`, `ReadXML(ctx, v)`; `WriteTo(w)` and
  `GobEncode()` keep their standard library signatures.
- On `File` the ctx-taking set is `ContentHash`, `ReadAll*`, `WriteAll*`,
  `WriteJSON`, `WriteXML`, `Append*`, `Truncate`, `ListDir*`, `ListDirIter*`,
  `ListDirMax*`, `ListDirRecursive*`, `Glob`, `MustGlob`, `MoveTo`,
  `RemoveRecursive`, `RemoveDirContents*`. Everything else (`Stat`, `Info`,
  `Exists`, `IsDir`, `Size`, `Modified`, `Permissions`, `Open*`, `Touch`,
  `MakeDir`, `MakeAllDirs`, `Remove`, `Rename*`, `User`/`Group`, `*XAttr`,
  symlinks, `Watch`, `IsEmptyDir`, `IsReadable`, `IsWritable`, `IsHidden`) is
  ctx-less.
- Listing keeps the callback (`ListDir(ctx, cb, patterns...)`), iterator
  (`ListDirIter(ctx, patterns...)`) and slice (`ListDirMax(ctx, max,
  patterns...)`) idioms. `ListDirChan`/`ListDirRecursiveChan` are dropped. One
  package-level `errStopListing` sentinel replaces the per-call sentinels
  (fixes the empty-string `SentinelError` in `ListDirIterContext`).
- Dropped: `File.GobEncode`/`GobDecode` (encoding a struct with a `File` field
  silently reads the whole file; `MemFile` keeps gob), `MemDir`,
  `FileInfoCache` from the public API (becomes an internal, mutex-protected
  cache in dropboxfs), `ReadOnlyBase`, `FullyFeaturedFileSystem`, the optional
  methods of `InvalidFileSystem`, the bogus type parameter of `SortByModified`
  (signature fixed), the `tests/` package (merged into the conformance suite).
- `IsWritable` is true for an existing writable regular file or directory and
  documents that it checks mode bits, not effective access.
- `Rename` on backends without native Rename/Move keeps copy+delete; `MoveTo`
  resolves "into directory" before dispatch.
- `MemFile`: shape unchanged; `Stat()` returns a deterministic zero `ModTime`
  (not `time.Now()`); the doc comment no longer claims `json.Marshaler`.
- `MemFileSystem`: the id is a counter/random string, not a heap address; the
  prefix ends with the separator; `WithID`/`WithVolume` become constructor
  options (no unlocked mutation after registration); writer/reader aliasing
  and lock gaps fixed; `JoinCleanPath` is separator-aware.
- `LocalFileSystem`: use the receiver's defaults instead of the `Local`
  singleton; no stderr writes (the Windows hidden-attribute lookup moves into
  `Stat`/`ListDir`); tilde expansion once in `CleanPath`; `Watch` expands the
  tilde and maps errors; the watcher goroutine is stopped on `Close`;
  compile-time interface assertions; `MakeAllDirs`/`RemoveAll` use
  `os.MkdirAll`/`os.RemoveAll`; `ID()` = real file system id of the root volume (`statfs` `f_fsid` /
  Windows volume serial), obtained once via `sync.Once`.
- `temp.go`: `TempFile` documented as path-only; add `CreateTempFile` (atomic,
  wraps `os.CreateTemp`); `MakeTempDir` uses `os.MkdirTemp`.
- `copy.go`: guard `src == dest`. `stdfs.go`: `checkStdFSName` uses
  `iofs.ValidPath`.

### Backends

- **s3fs:** one `key(path)` helper (`TrimPrefix "/"`) fixing the write/list
  mismatch (objects are written with a leading slash but listed without);
  un-gate `Test_fileSystem` (`GOFS_S3_CONFORMANCE`) once the conformance
  suite passes;
  implicit-directory `Stat` via `ListObjectsV2 MaxKeys=1`; `Remove` reports
  NotExist and refuses non-empty directories; `RemoveAll` via `DeleteObjects`;
  streaming `OpenReader`; drop the `Watch` stub; `closed` flag under a mutex;
  configurable multipart thresholds; fix the `Name()` test literal.
- **sftpfs:** ctx into `getClient` for the ctx-taking operations; native
  `MakeAllDirs` (`MkdirAll`), `ListDirRecursive` (`Walk`), `Symlink`/`ReadLink`,
  `SetPermissions` (`Chmod`); honor `perm` in `openFile`; alt prefix `:22`;
  never downgrade to `AcceptAnyHostKey` on reconnect; either make reconnect
  reachable (detect a broken client) or delete it together with
  `isConnectionError`.
- **ftpfs:** mutex around the single `ServerConn` (or a small connection pool);
  ctx; alt prefixes `:21`/`:990` and a consistent FTPS port; `AbsPath` uses the
  instance prefix; replace reply-text substring matching with
  `textproto.Error` code checks; `Remove` no longer falls back to `RemoveDir`
  for files; TLS verification on by default with an explicit
  `InsecureSkipVerify` option; share the credentials/`EnsureRegistered` layer
  with sftpfs via `fsimpl`.
- **dropboxfs:** ctx checked before every SDK call; `ID()` (account id) fetched
  once by the constructor, which takes ctx and may fail; typed `not_found`
  detection only (no substring match); `MakeDir` on an existing path maps to
  `ErrExist`; `Touch` on an existing file returns `ErrUnsupported`; native
  `RemoveAll`; mutex-protected internal info cache; streaming reader.
- **httpfs:** injectable `*http.Client` (package var + constructor),
  `http.NewRequestWithContext`; `PathHelper{Rooted:false}` fixing `Join`
  producing `http:///h/x/y`; `OpenReader` streams the GET body and does not
  HEAD first; `Close` documented as a no-op; not a `WriteFileSystem`.
- **multipartfs:** real `Size`/`Modified` from the `FileHeader`; delete the
  `EscapePath` stub (or apply it consistently); idempotent `Close`;
  `FileInfo.File` carries the prefix; real tests.
- **zipfs:** split into `ReaderFileSystem` and `WriterFileSystem` types (no
  mode branches); `dirtree` panics become errors and handle `a` + `a/b` and
  trailing-slash entries; entry map built at open (no O(n) `findFile`);
  `FileInfo.File` carries the prefix; `Stat` reports `ErrFileSystemClosed`
  after `Close`.
- **uuiddir:** fix `Make` (it creates `baseDir` instead of `uuidDir`), the
  `RemoveDir` boundary check (`/base` matches `/basement`), `Enum` no longer
  aborts on one unparsable directory; add `Test_Make`.

### Test infrastructure and CI

- Move `RunFileSystemTests` and `tests/filereads.go` into `fstest` so `testing`
  and testify leave the root package's non-test build. New suite
  `fstest.RunConformance(t, fs FileSystem, opts)`: seeds content through the
  file system when writable, otherwise takes a caller-provided seeded tree,
  and runs the read tests on every backend; error-contract section;
  ctx-cancellation section; `FileInfo` invariants; `CleanPath` purity;
  `OpenAppendWriter`/`OpenReadWriter`/`CopyFile` content verified; optional
  interfaces asserted, not logged; cleanup via `t.Cleanup`.
- `fstest.MockFileSystem` regenerated for the new signatures (embeds
  `PathHelper`; adds the missing RelPath/Symlink/XAttr hooks).
- CI: `go test -race` for every `go.work` module (via `test-workspace.sh`),
  `go vet`, staticcheck and gosec, on ubuntu/macos/windows; CodeQL kept and
  modernised. Docker-backed integration tests (MinIO, sshd, vsftpd) run in a
  separate job that skips when Docker is absent; Dropbox runs only with a repo
  secret. Tests that hit public internet servers are gated behind
  `GOFS_ONLINE_TESTS=1`.

## Phases

One PR per phase unless noted.

### Phase 0 — Roadmap, CI, baseline

- [x] This document replaces `TODOS.md`; repo `CLAUDE.md` records the errors
      decision.
- [x] `.github/workflows/test.yml` (test/vet/staticcheck/gosec matrix on three
      OSes, every module); CodeQL workflow modernised; `test-workspace.sh` runs
      with `-race`, staticcheck and gosec (staticcheck pinned in `tools`).
- [x] Internet-dependent tests gated or replaced with `httptest`; s3fs `Name()`
      test literal fixed; data race in `TestFile_Watch` and the staticcheck
      findings fixed.
- [x] Verify: CI green on ubuntu and macOS for all modules; the Windows build
      compiles and vets `localfilesystem_windows.go` for the first time.
      The Windows *test* job is `continue-on-error` for now: it had never run
      before and fails in `Test_FileJoin`, `Test_Join`, `TestFile_Glob`,
      `TestGlob`, `TestFile_Watch` (rename), `TestLocalFileSystem/
      RenameNonEmptyDir`, `TestSourceFile`, `TestStdFS`, `TestZipFileSystem`,
      `TestZipWriter_*` and `uuiddir` (separator and drive-letter
      assumptions). Making the Windows job blocking is part of Phase 4
      (local file system rework).

### Phase 1 — Conformance suite v2 and the bugs it finds

- [x] `fstest/conformance.go` (`fstest.RunConformance`, replacing
      `RunFileSystemTests` and `tests/filereads.go`) with the sections listed
      above; callers for Local and Mem (both separators) in
      `conformance_test.go`, `s3fs`, `sftpfs`, `ftpfs`, `dropboxfs`, plus new
      callers in `httpfs` (httptest server), `zipfs` (reader + writer) and
      `multipartfs` (synthetic form). `tests/` deleted; `testing`/testify no
      longer compiled into the root package.
- [x] Fixed what the suite surfaced: `JoinCleanPath` mutating its input
      (fsimpl, local, mem, invalid); mem `\` separator cleaning, Remove of
      non-empty directories, Stat/reads following symlinks,
      `ErrFileSystemClosed` after Close; httpfs `http:///host` URLs and
      unreadable file infos; zipfs `FileInfo.File` without prefix,
      `ListDirInfoRecursive` listing directories and panicking on
      conflicting entries, `Remove`/`Stat` errors; multipartfs fake sizes and
      times, `FileInfo.File` without prefix, listing errors, non-idempotent
      Close; sftpfs `MakeDir` on existing paths and listing a file;
      `uuiddir.Make`, `RemoveDir` boundary check, `Enum` aborting.
- [x] Verify: `./test-workspace.sh` green; the suite runs on Local, Mem,
      httpfs, zipfs, multipartfs offline and on sftpfs and ftpfs with Docker.
      **s3fs is the exception:** its conformance run is skipped unless
      `GOFS_S3_CONFORMANCE=1` because it fails on the object key slash
      mismatch and the implicit directory semantics (Stat/Exists of listed
      directories, `MakeDir` on existing, `Remove` of missing, `CopyFile`
      onto itself, emulated append/truncate reading stale keys, markers left
      after cleanup). All of that is the Phase 5 s3fs rework; un-gate the test
      there. dropboxfs still needs a token to run.

### Phase 2 — `fsimpl.PathHelper` and de-duplication

- [x] `fsimpl/pathhelper.go` (`fsimpl.PathHelper`: `URIPrefix`, `AltPrefixes`,
      `PathSep`, `Rooted`, `VolumeLen`) with tests for `/`, `\`, volume,
      rooted/unrooted, alt prefixes, non-mutation, idempotence.
- [x] s3fs, sftpfs, ftpfs, dropboxfs, httpfs, multipartfs, zipfs and
      `MemFileSystem` embed it; `InvalidFileSystem` delegates to it. Removed
      ~90 duplicated path methods. Behaviour changes: `CleanPathFromURI`
      always cleans; sftpfs/ftpfs accept URIs with the default port; `AbsPath`
      of sftpfs/ftpfs returns a rooted path instead of a URI; sftpfs, ftpfs
      and httpfs use the dot rule for `IsHidden` (was always false);
      `InvalidFileSystem` paths are unrooted like httpfs. URL unescaping
      still happens in `CleanPath`; moving it to `ParseRawURI` is part of
      Phase 3.
- [x] `fsimpl.NewWriteOnCloseFileBuffer` replaces the seven self-referential
      closures; `FileBuffer.WriteAt` rejects negative offsets and honors the
      `io.WriterAt` contract, `FileBuffer.Truncate` added, `Stat` without a
      `FileInfo` returns an error; dead exports removed
      (`DirEntryFromFileInfo`, `NewReadonlyFileBufferWithClose`,
      `InvalidateBuffer`). A shared closed-state guard was not worth a helper
      (two identical one-liners); revisit with the Phase 3 interface flip.
- [x] Verify: conformance (Local, Mem, httpfs, zipfs, multipartfs, sftpfs and
      ftpfs via Docker) and `fsimpl` tests green.

### Phase 3 — `FileSystem` interface flip (implementer-facing)

- [ ] Refactor `file.go`/`copy.go`/`fs.go`/`stdfs.go` so every backend call goes
      through unexported dispatch helpers (`fsStat`, `fsOpenReader`, ...);
      pure refactor, green.
- [ ] Introduce the new `FileSystem`/`WriteFileSystem`/optional interfaces;
      keep the old one as `LegacyFileSystem` with
      `AdaptLegacy(LegacyFileSystem) FileSystem` so the four external modules
      keep compiling. Same commit migrates `LocalFileSystem`, `MemFileSystem`,
      `InvalidFileSystem`, `httpfs`, `zipfs`, `multipartfs` and the `fstest`
      mocks (the root module must compile), deletes `ReadOnlyBase`, and changes
      the four sub-modules' `fs.Register(x)` to `fs.Register(fs.AdaptLegacy(x))`.
- [ ] Registry: `RLock` for reads; match on `AltPrefixes`; `Close` contract.
- [ ] Verify: conformance on all backends; `file_mock_test.go` ported. Tag
      `v0.2.0`.

### Phase 4 — `File`/`FileReader`/`MemFile` v1 API (consumer-facing)

- [ ] Apply the ctx rule to `File`, `FileReader`, `MemFile`; drop the twins,
      `ListDirChan`, `MemDir`, `File` gob, `FileInfoCache`,
      `FullyFeaturedFileSystem`; fix `SortByModified`; `IsWritable` semantics;
      `Touch` fallback; `MoveTo` into-dir; `temp.go`/`copy.go`/`stdfs.go` items;
      the `LocalFileSystem` and `MemFileSystem` fixes listed above.
- [ ] Windows test suite green (see the Phase 0 list) and the Windows CI job
      made blocking (`continue-on-error` removed).
- [ ] `docs/MIGRATION_v1.md` with sed/gofmt recipes (`.ReadAllContext(` →
      `.ReadAll(`, `.ReadAll()` → `.ReadAll(ctx)`, `.WriteAll(` →
      `.WriteAll(ctx, `, `.ListDir(` → `.ListDir(ctx, `, `.ContentHash()` →
      `.ContentHash(ctx)`, `.RemoveRecursive()` → `.RemoveRecursive(ctx)`, ...).
- [ ] Verify: conformance green; smoke-compile domonda-service and go-docdb
      against this branch via a temporary `replace` to validate the migration
      recipes (do not commit the replace).

### Phase 5 — Backends (one PR each: s3fs, sftpfs, ftpfs, dropboxfs)

- [ ] Per-backend changes listed above; each PR migrates the backend off
      `AdaptLegacy` and runs its conformance (Docker where needed).
- [ ] After the last one, delete `LegacyFileSystem`/`AdaptLegacy`. Tag `v0.3.0`.

### Phase 6 — Docs and release

- [ ] README rewrite for the v1 API (support matrix regenerated from
      conformance), package docs, s3fs README.
- [ ] CHANGELOG `v1.0.0` (Added/Changed/Removed + migration link), `VERSION` =
      `v1.0.0`, tag all modules in lockstep (`v1.0.0`, `s3fs/v1.0.0`,
      `sftpfs/v1.0.0`, `ftpfs/v1.0.0`, `dropboxfs/v1.0.0`, `tools/v1.0.0`).

## Verification (overall)

- `./test-workspace.sh` (with `-race`) green locally; GitHub Actions green on
  ubuntu/macos/windows for every module.
- `fstest.RunConformance` passes on Local, Mem (both separators), httpfs
  (httptest), zipfs reader + writer, multipartfs, s3fs (MinIO), sftpfs
  (Docker), ftpfs (in-process + Docker), dropboxfs (token).
- `go vet`, staticcheck and gosec clean.
- domonda-service and go-docdb compile against the v1 branch after applying
  `docs/MIGRATION_v1.md`.
- `grep -rn 'TODO\|FIXME' --include='*.go'` returns nothing unaddressed; no
  commented-out code blocks remain.
