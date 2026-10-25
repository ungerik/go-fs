# TODOS

Open work after the v1.0 pre-release fixes of 2026-10-25. The release items
come first; everything else is additive or internal, so it can land in a
v1.x release without breaking the v1.0 API. The decisions that freeze with
v1.0.0 are recorded in `CHANGELOG.md`, `docs/MIGRATION_v1.md` and the
reference docs in `docs/`.

## Release v1.0.0

### Verify azureblobfs against real Azure

**What:** Run the azureblobfs tests and conformance suite against a real
Azure storage account.

**Why:** The SDK sends x-ms-version 2026-12-06, which only Azurite with
`--skipApiVersionCheck` accepts, so the real service has not answered this
version yet. The snapshot handling, the ETag-pinned readers and the
ETag-pinned native `Move` were only tested against Azurite.

**Context:** Azurite 3.37 rejects the API version without the flag. Check
the snapshot listing order and paging (Azurite drops a blob's snapshots at a
page boundary), the 412 codes of failed source conditions (`ConditionNotMet`
on Azurite, `SourceConditionNotMet` documented for Azure) and the deletes of
blobs with snapshots.

**Effort:** S
**Priority:** P0
**Depends on:** An Azure storage account and credentials

### Verify dropboxfs with a real token

**What:** Run the dropboxfs tests with `DROPBOX_ACCESS_TOKEN` set.

**Why:** The offline fake client covers the code paths, not the real API.

**Context:** The token-gated tests skip without the variable.

**Effort:** S
**Priority:** P0
**Depends on:** A Dropbox access token

### Run the /cso security audit

**What:** Run the /cso skill on the release candidate.

**Why:** A dedicated security pass before the API freeze, after the
confinement, redaction and data-loss fixes of the betas and the pre-release
review.

**Context:** Requested by the maintainer.

**Effort:** S
**Priority:** P0
**Depends on:** None

### Check the release verification list

**What:** Confirm the release checklist on the final commit.

**Why:** It is the definition of done for v1.0.0.

**Context:**
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

**Effort:** S
**Priority:** P0
**Depends on:** The other release items

### Release v1.0.0

**What:** CHANGELOG `[1.0.0]`, `VERSION` = `v1.0.0`, the MIGRATION intro
names v1.0.0 instead of the betas, then tag all modules with
`tag-release.sh` after the merge (never the `tools` module).

**Why:** The release itself.

**Context:** `tag-release.sh` creates the lockstep tags and the GitHub
release from the CHANGELOG section.

**Effort:** S
**Priority:** P0
**Depends on:** The other release items

### Update the dependents

**What:** Update the repositories that use go-fs to v1.0.0.

**Why:** They are on betas, v0.1.0 or older pseudo-versions.

**Context:** The list of dependents is tracked outside this repository.
`docs/MIGRATION_v1.md` lists the changes; most of them fail to compile, so
`go build ./...` finds the call sites.

**Effort:** L
**Priority:** P1
**Depends on:** Release v1.0.0

## fs (core)

### Check destination names before a copy or move between file systems

**What:** A copy or move into a file system that rejects some names, like
the backslash rule of sftpfs, ftpfs and webdavfs, fails at the first such
name, after the files before it were copied.

**Why:** Moving from S3 or mem into sftpfs, ftpfs or webdavfs with a name
containing a backslash leaves a partial copy that a retry trips over.

**Context:** `treeCopier.checkSource` already checks every file of the
source before the first write, with `treeCopier.validName` for separators
and the local name rules. An optional backend interface like
`ValidName(name string) error` would plug into `validName`; adding it is not
breaking. The partial copy is documented in `docs/MIGRATION_v1.md`.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Bound the memory of the collision check of copies

**What:** When `CopyRecursive` copies into an existing local directory, it
`Lstat`s every entry it writes and keeps its whole `os.FileInfo` until the
directory is done.

**Why:** A flat directory of a million files holds about 300 MB of
`FileInfo`, and an overwrite costs up to three `Lstat`s of one path.

**Context:** Keep a compact identity instead (device and inode on Unix,
volume and file index on Windows, behind a build-tagged helper), and pass the
`FileInfo` of the destination check on instead of `Lstat`ing again.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Write local copies without following a swapped-in link

**What:** Copies check the destination with `Lstat` and then open it by path.

**Why:** A concurrent writer can swap in a link between the check and the
open, so the copy writes through it.

**Context:** Documented as a limit. A real fix for Local needs `os.Root` or
`O_NOFOLLOW` based writes.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Content hashes from metadata

**What:** An optional interface, tagged with the hash algorithm, that lets a
backend answer a content hash from its metadata.

**Why:** Dropbox stores the Dropbox content hash (`content_hash`) of every
file, S3 stores checksums, so the hash wouldn't need a download.

**Context:** `fs.DefaultContentHash` stays the Dropbox algorithm, computed
from the content. Only Dropbox stores that algorithm; S3 (ETag, CRC64NVME
and other checksums), Azure (Content-MD5 only for single-request uploads)
and zip (CRC-32) store others, under conditions. Go can't compare function
values, so the interface needs an algorithm name.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Remaining fsimpl.RedactURI edge cases

**What:** `http://localhost:8080/me@example.com` is redacted to
`http://localhost:xxxxx@example.com`, and a password like `bob:12/50%off@host`
is not redacted in fs/httpfs messages.

**Why:** Error messages either hide the wrong part or keep a password.

**Context:** sftpfs, ftpfs and smbfs reject such addresses; webdavfs uses
`RedactAddress`.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Reject Unicode look-alikes of the backslash on Windows servers

**What:** `fsimpl.CheckNoBackslash` rejects only U+005C.

**Why:** Windows servers that convert names to an ANSI code page with
best-fit mapping turn U+FF3C, U+FE68, U+2216 and, under CP932, U+00A5 into a
backslash, so such a name could still be split at a separator. The review
rated this low confidence (3 of 10).

**Context:** Either reject the common best-fit equivalents unless
`AllowBackslashInNames` is set, or document the residual case at the option
of sftpfs, ftpfs and webdavfs.

**Effort:** S
**Priority:** P4
**Depends on:** None

### Skip short forward gaps in fsimpl.RangeReader

**What:** `fsimpl.RangeReader` reopens the body on every seek.

**Why:** tarfs over azureblobfs or webdavfs costs one GET per entry.

**Context:** Skip short forward gaps with `io.CopyN` instead of reopening.

**Effort:** S
**Priority:** P3
**Depends on:** None

## ftpfs

### Detect links on servers with MLST support

**What:** With MLSD/MLST, `jlaffaye/ftp` reads `type=OS.unix=slink:…` as a
file and drops the target, and servers like ProFTPD report the facts of the
target, so ftpfs can't tell links from files and directories there.

**Why:** `ListDirRecursive` walks a link to a directory, and a link to an
ancestor until the server fails to resolve the path.

**Context:** `RemoveAll` is safe: it tries `DELE` before it deletes the
content of a directory and confirms a directory with `LIST` of the parent
over a second connection. A real fix needs raw MLSD access, which the client
library doesn't offer.

**Effort:** M
**Priority:** P2
**Depends on:** MLSD facts in `jlaffaye/ftp`

### Resolve a link at the listed path once in ListDirRecursive

**What:** `ListDirRecursive` of a symbolic link resolves the link three times.

**Why:** On servers without MLST every resolution step is a LIST of the
parent directory over a new data connection: three listings of the link's
parent and two of the target's parent where one each would do.

**Context:** `ListDirRecursive` calls `f.stat`, which resolves the link and
drops the target path, then `list(dirPath)` and `listedItself`, which calls
`lstat` and `resolve` again. Split `resolve` so that `listedItself` continues
from its own `lstat` result, and list the resolved target directly in
`ListDirRecursive`. `ListDir` repeats one `lstat` inside `resolve` as well.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Tell links from directories on Windows FTP servers

**What:** On Windows FTP servers (IIS with DOS-format listings, FileZilla
Server or IIS in Unix style, where junctions and directory symlinks appear as
directories) and on ProFTPD with `ShowSymlinks off`, LIST shows a link to a
directory as a directory.

**Why:** `RemoveAll` confirms a directory with LIST before it deletes the
content, so on these servers it deletes through such links, as before v1.0.
DELE of a junction always fails, so every junction reaches the confirmation.

**Context:** Documented in the package doc. Trust the confirmation only for
listings in a format that can show links (`SYST` or the ls-style lines) and
fail closed otherwise, or find another way to detect reparse points.

**Effort:** M
**Priority:** P2
**Depends on:** None

### Separate TLS sessions for the extra FTPS connections

**What:** The LIST connection of `RemoveAll` and the dedicated connection of
`OpenReader` share the TLS config, and with it one session cache slot per
server name, with the main connection.

**Why:** After their handshake the main connection's next data connection can
resume their session instead of its own, and servers that bind data
connections to the session of their control connection (FileZilla Server,
ProFTPD) may reject it with 425/522.

**Context:** Give each control connection a copy of the TLS config with its
own session cache, and add a test-server mode that requires a data connection
to resume the session of its own control connection.

**Effort:** S
**Priority:** P2
**Depends on:** None

### Stream ListDirRecursive per directory

**What:** `ListDirRecursive` collects the whole tree on the control
connection before the first callback.

**Why:** A large tree holds the connection and memory until it is listed.

**Context:** List per directory and release the connection in between.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Fewer commands for RemoveAll on servers with MLST support

**What:** On servers with MLST support `RemoveAll` tries `DELE` for every
directory before it deletes the content, and after the failed `DELE` it
lists the parent with `LIST` over a second connection that it dials for the
call.

**Why:** It is how a link that MLST describes as a directory gets deleted
itself, and how a real directory is confirmed, but it costs a failing
command per directory, a LIST per parent directory and an extra login per
`RemoveAll`. For the removed path itself the LIST lists its parent, which can
be large: removing the N children of a directory one by one lists it N times.
A directory with subdirectories is listed twice, with MLSD to enumerate it
and with LIST to confirm its subdirectories.

**Context:** The trade-off is documented in the package doc. Options: keep
the LIST connection open across calls, skip the confirmation when the DELE
reply says "Is a directory" (unlink of a link never fails with EISDIR), or
enumerate with the LIST listing of the second connection, which tells links
from directories and needs no MLSD. Skipping DELE below the top level would
also skip the link check for nested links.

**Effort:** M
**Priority:** P3
**Depends on:** None

## s3fs and azureblobfs

### An option to skip the directory check before writes

**What:** s3fs and azureblobfs send a listing request before every write to
check that the destination is no directory.

**Why:** It doubles the cost of small writes (a Class A request).

**Context:** Skip it when the caller just stat'ed (copies, moves), or add an
`Options` flag.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Don't trust the S3 paginator on a truncated page without a token

**What:** s3fs `ListDir`, `ListDirRecursive` and `RemoveAll` use the SDK
paginator, which ends silently at a truncated page without a continuation
token.

**Why:** A broken S3-compatible store can make them return an incomplete
result, or leave objects behind, without an error.

**Context:** `findObject` already checks this for `Stat` and `Remove`; a
three-line check per paginator loop.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Bound the memory and parallelize the requests of a directory Move

**What:** The native `Move` of an azureblobfs directory keeps the name of
every blob in memory and copies and deletes one blob per sequential request.

**Why:** A tree of millions of blobs holds every name until all copies and
deletes finish, and N blobs take 2N round trips one after another.

**Context:** `RemoveAll` caps its name list at `maxRemoveAllNames` and lists
again beyond it; `Move` could do the same, copying page by page from a second
listing and deleting from a third. Copies can run with a small worker pool,
deletes with the blob batch API (256 per batch) where the credential supports
it, also for `RemoveAll`, which sends one DELETE per blob today.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Back off while polling a pending server-side copy

**What:** azureblobfs `copyBlob` polls `GetProperties` every 100 ms while a
copy is pending, without backoff and without a limit other than ctx.

**Why:** A slow server-side copy costs about 10 paid requests per second per
blob, and nothing stops it under a context without a deadline. The native
`Move` calls `copyBlob` once per blob.

**Context:** Back off exponentially from 100 ms to a few seconds, honour
`Retry-After`, and optionally give up after a configurable maximum wait.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Pin the azureblobfs listing format to XML

**What:** Set the listing `ResponseFormat` explicitly to XML.

**Why:** A changed SDK default would break the listing parser assumptions.

**Context:** Left opt-in after beta.3.

**Effort:** S
**Priority:** P4
**Depends on:** None

## sftpfs, smbfs, webdavfs, uuiddir

### Concurrent writes in sftpfs

**What:** Enable `sftp.UseConcurrentWrites`.

**Why:** Uploads are capped at 32 KiB per round trip.

**Context:** In the client options of the SFTP session.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Cover smbfs links and uuiddir directory links

**What:** smbfs link behaviour is not covered by the conformance link check,
and uuiddir walks a link to a directory like a directory.

**Why:** The test Samba server rejects links ("not a reparse point"), so the
link contract is unverified for smbfs; uuiddir's fixed five-level walk is not
part of the `ListDirRecursive` link rule.

**Context:** A Samba configuration with unix extensions or a Windows server
for smbfs; uuiddir should skip links like `ListDirRecursive`.

**Effort:** M
**Priority:** P3
**Depends on:** None

### webdavfs base URLs with a port and a raw @ in the path

**What:** webdavfs rejects base URLs without user info that have a port and
a raw `@` in the path.

**Why:** Nextcloud email users on a non-default port must write `%40`.

**Context:** `url.Parse` would take a username with a password of digits.

**Effort:** S
**Priority:** P4
**Depends on:** None

## Completed
