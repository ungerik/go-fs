# TODOS — Road to v1.0

Findings from a pre-1.0 audit of the whole module tree. Grouped by severity:
🔴 blocker (data loss / corruption / crash / security), 🟠 should-fix (API
correctness & consistency, decide before freezing the API), 🟡 nice-to-have.
File:line references are approximate and may drift as code changes.

## Release process

- [x] Add a `VERSION` file — done (`v0.1.0`, the v1.0-prep release; v1.0.0
      comes later).
  - [ ] At release, tag all modules in lockstep with the `VERSION` value
        (`v0.1.0`, `s3fs/v0.1.0`, `sftpfs/v0.1.0`, `ftpfs/v0.1.0`,
        `dropboxfs/v0.1.0`). No version tags exist yet.
- [x] Add a CHANGELOG — done (`CHANGELOG.md`, v0.1.0 entry).
- [x] Extend the conformance suite to cover the cases that hid the data-loss
      bugs above: overwriting a larger file with smaller data (truncation) and
      renaming a non-empty directory. **Done:** `RunFileSystemTests` now has
      `OverwriteShrink` (OpenWriter, native WriteAll and high-level
      `File.WriteAll` must all truncate) and `RenameNonEmptyDir` subtests, and
      registers the FS under test so the high-level File API paths run on every
      backend. Passing on local, mem, s3fs and sftpfs (ftpfs/dropboxfs
      conformance need a live server/credentials to run).
- [ ] Run the shared conformance suite against the read-only backends
      (`httpfs`, `zipfs` reader mode, `multipartfs`). Blocked on suite design:
      the read tests are gated behind the writable check (`filesystemtests.go`,
      `if !writable { return }`), so a read-only FS currently only exercises the
      metadata/path/pattern subtests. Running them meaningfully needs the suite
      restructured to seed content and verify read paths for read-only file
      systems. (`uuiddir` is a UUID directory-layout helper, not a `FileSystem`,
      so the suite does not apply to it.)
- [x] `ftpfs` now has a Docker-free in-process FTP server test plus closed-state
      and `EnsureRegistered` ref-count tests (and a Docker-based conformance run).
- [ ] `dropboxfs` has error-mapping and closed-state unit tests now; its full
      `RunFileSystemTests` conformance run still needs a real Dropbox access
      token to execute in CI.
- [ ] Flesh out `TestFileReads`, `TestFileMetaData`, and add `TestFileWrites`
      with comprehensive coverage across backends.

## 🔴 Blockers — data loss / corruption / crashes

- [x] **`WriteAll` fallback never truncates.** `WriteAllContext`
      (`file.go:1301`) fell back to `OpenReadWriter`, which is documented as
      non-truncating (`localfilesystem.go:663`, `O_RDWR|O_CREATE`). Writing
      fewer bytes over a larger existing file left stale trailing bytes (the
      classic small-JSON-over-big-JSON → invalid JSON). Reachable on `sftpfs`
      and `ftpfs` (writable, no native `WriteAll`). **Fixed:** the fallback
      now uses `OpenWriter` (`O_TRUNC`). Covered by
      `TestFile_WriteAllContext_FallbackTruncates` (`file_test.go`), which
      exercises the fallback through a `MockFileSystem` that lacks native
      `WriteAll` and reproduces the stale-trailing-bytes corruption.
- [x] **`File.Rename` on a non-empty directory discards contents.** Default
      branch (`file.go:1457-1467`): for an FS without `RenameFileSystem`/
      `MoveFileSystem` it did `renamedFile.MakeDir()` (an empty dir) then
      `file.Remove()`, never copying the contents, then failed to remove the
      non-empty source. **Fixed:** the fallback now uses `CopyRecursive` +
      `RemoveRecursive` (mirroring `Move` in `fs.go`), so files and non-empty
      directories are moved completely; on a copy failure the source is left
      intact rather than half-deleted. Covered by
      `TestFile_Rename_DefaultBranch` (`file_test.go`), which routes a
      `MemFileSystem` through a wrapper exposing only the base `FileSystem`
      interface so `Rename` takes the default branch.
      Fixing this surfaced two latent `MemFileSystem` bugs (its native
      `Rename`/`Move` meant the recursive copy path was never exercised on it):
  - [x] **`MemFileSystem.ListDirInfo`/`ListDirInfoRecursive` left
        `FileInfo.File` empty**, so `File.ListDir`/`CopyRecursive` over an
        in-memory directory yielded invalid empty-path files. **Fixed:** both
        now set `File` via `JoinCleanFile` (matching `LocalFileSystem` and
        `ListDirMax`).
  - [x] **`MemFileSystem.ListDirInfo`/`ListDirInfoRecursive` held
        `fs.mtx.RLock()` across the user callback**, so a callback writing
        back into the same file system (e.g. `CopyRecursive`/`Move` within one
        `MemFileSystem`) deadlocked. **Fixed:** entries are snapshotted under
        the lock, which is released before any callback runs (matching the
        lock-free callback contract of `LocalFileSystem`). Both covered by
        `TestMemFileSystem_ListDir_PopulatesFileAndDoesNotDeadlock`
        (`memfilesystem_test.go`).
- [x] **`ftpfs` random-access `file` type is unsound** (`ftp.go` ~700-755):
      `Read`→`ReadAt(p, f.offset)` never advanced `offset` (re-read forever,
      never EOFed); each `ReadAt` opened a `RetrFrom` and leaked the
      `ftp.Response`; each `Write` issued a fresh `Stor`/`StorFrom` so
      multi-chunk writes overwrote instead of appending. `OpenWriter` returned
      this type. **Fixed:** removed the `file` type. FTP has no random-access
      I/O, so `OpenWriter`/`OpenReadWriter` now use an in-memory
      `fsimpl.FileBuffer` flushed with a single `STOR` on `Close` (matching
      `s3fs`/`dropboxfs`), and native `ReadAll` (`RETR`), `WriteAll` (`STOR`),
      `Append`/`OpenAppendWriter` (`APPE`) were added. Covered by Docker-free
      in-process FTP server tests (`ftpfs/testserver_test.go`,
      `Test_fileSystem_InProcess`).
- [x] **`dropboxfs.info()` and `httpfs.info()` turn every error into "does
      not exist."** Auth failure, rate-limit, 5xx, network outage all returned
      `Exists:false` → `Stat` reported `ErrDoesNotExist`
      (`dropboxfs.go:259-263`, `http.go:109/130/146`). `httpfs.info()` also
      ignored HTTP status (a 404 with a body reported `Exists:true`), so
      `Exists()` and `OpenReader()` disagreed. **Fixed:** both `info()` now
      return `(info, error)` and only report `Exists:false` for a genuine
      not-found (Dropbox `LookupError.not_found` via typed detection, HTTP
      404/410); transport, auth, rate-limit and 5xx errors are propagated so a
      flaky backend no longer makes files vanish. `httpfs.info()` now honors
      the status code (2xx ⇒ exists, 404/410 ⇒ not exist, HEAD 405/501 ⇒ GET
      fallback, everything else ⇒ error), so `Exists()` and `OpenReader()`
      agree. `Exists()` still returns a bool (false when existence is unknown);
      use `Stat` to observe the error. Covered by `httpfs` `TestInfoStatusHandling`
      / `TestInfoTransportErrorNotMissing` (`http_status_test.go`) and
      `dropboxfs` `TestIsNotExistError` (`errors_test.go`).
- [x] **`s3fs` nil-pointer derefs** on `*out.ContentLength` /
      `*out.LastModified` in `Stat` (`s3fs.go:247-248`) and `OpenReader`
      (`s3fs.go:740-741`); these AWS-SDK fields are pointers and can be nil
      from S3-compatible servers. The same file guarded them elsewhere.
      **Fixed:** added `derefInt64`/`derefTime` nil-safe helpers and used them
      for every `ContentLength`/`LastModified`/`Size` dereference in `Stat`,
      `OpenReader` and `listDirInfo`, so a nil field yields 0 / zero-time
      instead of a panic. Covered by `TestDerefHelpers` (`s3fs/deref_test.go`).
- [x] **Return `ErrFileSystemClosed` from all closable `FileSystem`
      implementations.** Every `Close()` nilled its client/conn (`s3fs.go:958`,
      `sftp.go:610`, `ftp.go:689`, `dropboxfs.go:534`) but no method checked
      closed state before dereferencing, so post-`Close` calls panicked. The
      package already defines `ErrFileSystemClosed` (`errors.go:28`).
      LocalFileSystem is not closable. **Fixed:** each remote backend now has a
      `closed` flag set by `Close` (replacing the fragile `id==""`/`client==nil`
      sentinels). `s3fs` checks it at the start of every API method (and
      `Exists` returns false); `sftpfs`/`ftpfs` check it in the central
      `getClient`/`getConn` choke point, which crucially also stops the
      auto-reconnect/lazy-dial logic from resurrecting a closed connection;
      `dropboxfs` checks it via a `checkClosed` helper on every client method.
      `Close` is idempotent and now reliably unregisters even when the lazily
      populated client/id was never used. Covered by `TestClosedFileSystem` in
      `s3fs`, `sftpfs`, `ftpfs` and `dropboxfs`.
- [x] **`fsimpl.ReadWriteAllSeekCloser` leaks the underlying file.** Its doc
      claimed a `close` callback "will be called on Close()"
      (`readwriteallseekcloser.go:27`) but there was no close field and nothing
      was ever closed; `Close` also early-returned when `!modified`. **Fixed:**
      added the documented `close func() error` field and constructor parameter;
      `Close` now always invokes it (at most once, even when nothing was
      modified), writes back when modified, and joins the write-back and close
      errors. Both call sites (`fs.NewFileReadWriteAllSeekCloser`,
      `zipfs.OpenReadWriter`) pass `nil` since their closures self-close.
      Covered by `TestReadWriteAllSeekCloser_CloseCallback`
      (`readwriteallseekcloser_close_test.go`).
- [x] **`fsimpl.DropboxContentHash` uses `reader.Read` not `io.ReadFull`**
      (`contenthash.go:29`), so a legal chunking reader that returned <4 MB
      without EOF terminated the block loop early and computed the wrong hash.
      **Fixed:** the block loop now fills each 4 MB block with `io.ReadFull`
      (treating `io.EOF`/`io.ErrUnexpectedEOF` as end-of-input), so the hash is
      independent of how the reader chunks its output. Covered by
      `TestDropboxContentHash_ChunkingReader` (`fsimpl/contenthash_test.go`).

## 🟠 Should-fix — half-baked solutions & dead code

- [x] **`subfilesystem.go` (100% commented-out `sub://` mount) deleted.**
- [x] **`fsimpl/other.go` (60-line commented block) deleted.**
- [x] **`zipfs` writer mode no longer misuses `archive/zip.Writer`.** Was:
      `Touch` discarded the `Create` writer; `OpenWriter` returned a `nopCloser`
      that never finalized the entry; nothing enforced zip's sequential-write
      rule; `OpenReadWriter` had an unreachable "future-proof" dual-mode branch;
      `MakeDir` silently returned `nil`. **Fixed:** `OpenWriter` returns a
      `zipEntryWriter` tracked as the file system's single active writer;
      opening another entry (via `OpenWriter`/`Touch`) before closing it, or
      writing to a closed/superseded writer, now returns a clear error instead
      of corrupting the archive (all guarded by a `sync.Mutex`). `OpenReadWriter`
      lost its dead branch and reports the read-only/write-only reason;
      `MakeDir` is mode-aware (errors on read-only/closed, documented no-op for
      writer mode since zip dirs are implicit). Covered by
      `TestZipWriter_SequentialEnforcement` and
      `TestZipWriter_MakeDirReadOnlyErrors`.
- [x] **`s3fs.Name()`/`zipfs.Name()` placeholder strings fixed.** `s3fs.Name()`
      now interpolates the bucket name; `zipfs.Name()` reports "Zip writer
      filesystem" in writer mode.
- [ ] **`multipartfs` placeholders:** `EscapePath` is a stub (only replaces
      `"`, `multipartfs.go:311`, `TODO: properly escape paths`) and is applied
      inconsistently (used by `OpenReader`, not `Stat`/`Exists`); `info()`
      fabricates `Size:-1` and `Modified:time.Now()`
      (`TODO get time from header`, line 179) though the real size is on the
      `multipart.FileHeader`.
- [x] **`stdfs.go` no longer falsely advertises `io/fs.GlobFS`.** Removed the
      `GlobFS` line from the doc comment, the commented-out assertion, and the
      commented-out `Glob` method.
  - [ ] Still open (separate concern): `checkStdFSName` rejects valid names via
        `strings.Contains(name, "/.")` (blocks `dir/.gitignore`) — use
        `iofs.ValidPath`.
- [x] **Deleted remaining commented-out blocks:** `InfoWithContentHash*` in
      `file.go`, `ListDirRecursiveImpl` in `dirs.go`, `NameSizeProvider`/
      `nameSizeInfo` in `fileinfo.go`, and the dead `NewFileSystem`/
      `UsernameAndPasswordFromURL` in `sftpfs/sftp.go`.
- [x] **`EnsureRegistered` ref-count bug** in `sftpfs` and `ftpfs` fixed. The
      returned `free` could close an FS another caller still held: the "already
      registered" branch never closed (leaking when the creator freed first),
      the creator branch closed unconditionally, `Close` used an off-by-one
      threshold (closing while one reference remained), and a connection that
      lost the dial-then-register race was left orphaned while `free` closed the
      wrong instance by prefix. **Fixed:** both branches now return the same
      ref-count-aware `free` (`f.Close`), `Close` closes the connection only when
      the last reference is released (split out a registry-free `closeConn`), and
      a connection that loses the registration race is discarded immediately via
      `closeConn` while `free` drops the ref count of the winner. Covered by
      `TestEnsureRegistered_RefCount` (ftpfs, in-process server).

## 🟠 Should-fix — weak abstractions

- [x] **"No prefix ⇒ local" swallows unknown schemes.** `ParseRawURI`
      (`registry.go:137-155`) fell through to `Local` for any unmatched URI,
      so `File("s3://bucket/key")` without importing `s3fs` opened a local file
      literally named `s3://bucket/key` — no "unknown scheme" diagnostic.
      **Fixed:** `ParseRawURI` now uses the `PrefixSeparator = "://"` const: a
      URI that carries a scheme but matches no registered file system resolves
      to the `Invalid` file system (so the operation fails clearly) instead of
      `Local`. The only scheme that maps to local is `file://` (matched via the
      registered `Local` file system); scheme-less paths still resolve to
      `Local`. Covered by `TestParseRawURI_SchemeNotLocal` (`registry_test.go`).
- [x] **Optional-interface matrix rationalized and documented.** Guiding
      principle adopted: a backend implements an optional interface only when it
      is more efficient or more capable than the generic emulation. **Done:**
      added native `Touch` to `sftpfs` (via SFTP `SETSTAT`/`Chtimes`) and
      `ftpfs` (via FTP `MFMT`/`SetTime`) — the generic `Touch` opens with
      `O_TRUNC` and would destroy an existing file's content, so a native Touch
      that updates mtime in place (and only creates when missing) is a real
      benefit. Deliberately did NOT add `Exists`/`ReadAll`/`WriteAll`/`Append`
      to `sftpfs`, where they would be byte-for-byte identical to the generic
      emulation (`ftpfs` already gained native `ReadAll`/`WriteAll`/`Append` in
      the earlier FTP rewrite, so only `Touch` was new there). The per-backend
      support matrix is now documented in the README ("Optional interface
      support"). Covered by the `ftpfs` Touch in-process test and the
      conformance `FileTouch` subtest.

- [x] **Predicate methods return false on any error — by design.**
      `Exists`/`IsDir`/`IsReadable`/`IsWritable`/`IsRegular`/`IsEmptyDir`/
      `IsSymbolicLink` collapse any `Stat` error (permission, I/O) into the
      value consistent with the file/dir not existing (`false`). **Decision:**
      this is intended — the methods do not return errors. **Done:** documented
      the no-error contract and the default-on-error result on each method;
      callers needing the error use `CheckExists`/`CheckIsDir`/`Stat`.
  - [ ] Separate open sub-question (not about error handling): `IsWritable`
        reports `false` for an existing writable *directory* because it requires
        `IsRegular()` (`file.go:232`), and it only checks the mode bit, not
        whether this process can actually write. Decide the intended semantics
        before the freeze.
- [ ] **`perm []Permissions` variadic-as-optional-arg.** Consumed by
      `JoinPermissions` (`permissions.go:91`) which OR-merges all elements, so
      `OpenWriter(UserRead, OthersWrite)` silently means the union and
      `OpenWriter(0)` means `NoPermissions`, not default. Decide consciously
      for the freeze.
- [ ] **`ReadOnlyBase` is an incomplete mixin** (`readonlybase.go`):
      hardcodes `ReadableWritable() (true,false)`, only blocks the core write
      methods (not `Touch`/`WriteAll`/`Append`/`Truncate`/`Move`…), and
      bundles the read-only `MatchAnyPattern`.
- [x] **`MockFileSystem` moved out of the root public package.** It shipped in
      package `fs` with no build tag, joining the v1.0 compatibility promise and
      polluting the `fs.` namespace. **Done:** moved `MockFileSystem` and
      `MockFullyFeaturedFileSystem` to a new `github.com/ungerik/go-fs/fstest`
      package (mirroring `testing/fstest`), with all types `fs.`-qualified and
      the stale `MockFileSystemFullyFeatured` panic names corrected. The two
      mock-using tests moved to an external `package fs_test` file
      (`file_mock_test.go`) to avoid the `fstest`→`fs` import cycle.
- [ ] **Three parallel directory-listing idioms** (callback `ListDir`, channel
      `ListDirChan`, iterator `ListDirIter`) plus slice `ListDirMax` — consider
      deprecating the channel variants before freezing. Also unify the sentinel
      used by `ListDirIterContext` vs `ListDirRecursiveIterContext` — one is a
      zero-value `SentinelError` (empty string), making `errors.Is` unsound.
- [ ] **`FileInfoCache` is exported but not concurrency-safe**
      (`fileinfocache.go:30-63`, map mutated without a mutex).

## 🟠 Should-fix — ad hoc code & cross-cutting consistency

- [ ] **`ftpfs` parses FTP reply text to detect success:**
      `strings.Contains(err.Error(), "226 Transfer complete")`, `"257"`,
      `"150"`, `"227 Entering Passive Mode"` swallowed to `nil`
      (`ftp.go:477,564,737`). `"257"` matches any message containing it.
- [ ] **`ftpfs` FTPS uses `InsecureSkipVerify:true` + a no-op
      `VerifyPeerCertificate`** (`ftp.go:104-113`), hardcoded with no opt-out
      — encryption without server authentication.
- [ ] **`sftpfs` reconnect drops to `AcceptAnyHostKey` unconditionally**
      (`sftp.go:364`), silently downgrading host-key verification on
      reconnect.
- [ ] **`ftpfs` shares one `*ftp.ServerConn` with no mutex** (`ftp.go:69`) —
      concurrent ops interleave on the wire; no reconnect either.
      `sftpfs.getClient` has a TOCTOU read of `f.client` outside the lock
      (`sftp.go:342`).
- [ ] **`sftpfs.isConnectionError` does ad hoc substring matching** on 12
      lowercased error strings (`sftp.go:248-269`) to drive reconnect.
- [ ] **Context is ignored almost everywhere.** Interface methods without
      `ctx` (Stat, Open*, Remove, MakeDir, Touch) make uncancelable network
      calls in every remote backend; methods with `ctx` honor it only
      partially (s3 threads it; dropbox/http check `ctx.Err()` once then drop
      it — `http.go:182` has `// TODO use HTTP GET with context`). Local bulk
      ops check `ctx` once then block (`localfilesystem.go:571/588/604`).
- [ ] **Memory-buffered I/O in every remote backend** — s3/dropbox/http
      `Open*` load entire files into RAM despite streaming bodies being
      available.
- [ ] **Use `errs.New`/`errs.Errorf`** (`github.com/domonda/go-errs`) per repo
      rules instead of stdlib `errors`/`fmt` (used throughout). Promote
      per-call sentinels (`errors.New("done")` in `file.go:905`, `dirs.go:49`)
      to package-level consts.
- [ ] **Inconsistent not-exist / already-exists mapping** across backends:
      sftp maps nothing; dropbox matches bare substring `"not_found"`; s3
      `Remove` is idempotent so never reports not-exist. Make `errors.Is(err,
      os.ErrNotExist)` / `os.ErrExist` work uniformly.
- [ ] **`File.MakeAllDirs` and recursive remove ignore optimized local
      implementations** — `File.MakeAllDirs` always recurses per-level instead
      of dispatching to `MakeAllDirsFileSystem`/`os.MkdirAll`, and recursive
      remove never uses `os.RemoveAll` (`localfilesystem.go:818` Remove only
      deletes empty dirs). Bypasses OS umask fixups.
- [ ] **`localfilesystem` writes to `os.Stderr` from library code** on
      xattr-lookup error (`localfilesystem.go:254-267`, own
      `TODO panic or configurable logger`), while the same check in
      `ListDirInfo` returns an error — pick one behavior. `localfilesystem.go:92`
      `ID()` returns a placeholder `"/"`.
- [ ] **`zipfs`/`dirtree` panics on malformed/malicious zip input**
      (`dirtree.go:121,160`) — return an error instead.

## 🟡 Nice-to-have

- [ ] Add a typed `ErrFileSystemsDoNotMatch` for the recurring cross-FS
      mismatch condition (Move/Copy/Rename/RelPath/Symlink use raw
      `fmt.Errorf` today, e.g. `file.go:378,433`).
- [ ] Guard `CopyFileBuf` against src == dest (`copy.go`): the generic path
      opens the dest writer with `O_TRUNC` before reading src, so copying a
      file onto itself on a non-`CopyFileSystem` truncates it.
- [ ] `temp.go` `TempFile` returns a non-reserved random path (no atomic
      create) — two goroutines can get the same path; prefer
      `os.CreateTemp`/`os.MkdirTemp` semantics.
- [ ] Make s3 multipart thresholds (5/10 MB, `s3fs.go:31,35`) and sftp retry
      backoff configurable.
- [ ] `localfilesystem_unix.go SetGroup` (~87-104) calls `expandTilde` twice
      and checks empty after expansion — copy-paste rot vs `SetUser`.
- [ ] Fix doc/typo hygiene: `RandomString` "randum" (`fsimpl.go:13`),
      duplicated `MustGlob` doc (`file.go:585-610`). (The stale `MockFileSystem`
      panic names were corrected during the move to `fstest`.)
- [ ] `InvalidFileSystem.Close()` returns `ErrInvalidFileSystem`
      (`invalidfilesystem.go:229`) — a no-op `Close` conventionally returns
      nil. It also duplicates path logic from `fsimpl` inline; consolidate.
