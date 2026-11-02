# TODOS

Open work after the v1.0 pre-release fixes of 2026-10-25. The release items
come first; everything else is additive or internal, so it can land in a
v1.x release without breaking the v1.0 API. The decisions that freeze with
v1.0.0 are recorded in `CHANGELOG.md`, `docs/MIGRATION_v1.md` and the
reference docs in `docs/`.

## Release v1.0.0

### Verify dropboxfs with a real token

**What:** Run the dropboxfs tests with `DROPBOX_ACCESS_TOKEN` set.

**Why:** The offline fake client covers the code paths, not the real API.

**Context:** The token-gated tests skip without the variable. Also check
whether the API refuses names like `desktop.ini`, `thumbs.db`, `.ds_store`,
`icon\r`, `.dropbox` and `.dropbox.attr` (`disallowed_name`); then the
dropboxfs `ValidateName` has to reject them, so a copy of a Windows or macOS
folder fails before it writes anything.

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

### Close the how-to gaps of the v1.0 features

**What:** Several v1.0 behaviours are documented only in the reference docs,
the module READMEs and `docs/MIGRATION_v1.md`, not in the how-tos:
`ReadHeader(ctx, …)`, `fsimpl.CheckNoBackslash` (the tutorial for
implementing a file system), `Options.AllowBackslashInNames` of sftpfs,
ftpfs and webdavfs (the connect-a-backend how-to), the native azureblobfs
`Move`, `Options.DeleteSnapshots` and `ErrSnapshotsPresent` (the Azure
section of that how-to and of the README), the `os.ErrInvalid` of `CopyFile`
and `Move` for a source name that is invalid in the destination directory,
troubleshooting for the collision, special-file and invalid-name errors of
copies and moves, and the refusal of `Register` for a separator before
`://` (the Registration section of the path explanation).

**Why:** Readers of the how-tos don't learn them where they need them.

**Context:** Found by the documentation audit of v1.0.0-rc.1. Also fix two
older docs problems: `docs/reference-filesystem-interfaces.md` says only
sftpfs and ftpfs return a default-port prefix alias, but smbfs returns the
`:445` alias too, and the `multipartfs/README.md` example registers before
`defer formFS.Close()`, so a failed `Register` leaks the temporary files
(`docs/howto-handle-form-uploads.md` has the right order). And state the
prefix rule, a scheme followed by `://` with no separator before it, in the
Options section of `s3fs/README.md` and in the `Prefix` row of
`docs/reference-filesystem-interfaces.md`, which says only "Must not be
empty" while the conformance suite requires the scheme.

**Effort:** M
**Priority:** P2
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
names v1.0.0 instead of the rc, the `go get` lines in the Markdown files
drop their `@v1.0.0-rc.6` pins (`grep -rn '@v1.0.0-rc.6' --include='*.md'`)
and the README drops the note that plain `go get` installs v0.1.0, then tag
all modules with
`tag-release.sh` after the merge (never the `tools` module).

**Why:** The release itself.

**Context:** `tag-release.sh` creates the lockstep tags and the GitHub
release from the CHANGELOG section.

**Effort:** S
**Priority:** P0
**Depends on:** The other release items

## fs (core)

### Reject zero-width characters and device names that zip name decoding produces

**What:** A Unicode Path run or the CP-437 decoding can produce a name with
a zero-width character (U+200B, U+FEFF, U+2060) or a Windows device name
(`COM?` to `COM¹`) that `isDisguisingRune` and the stored name don't have.

**Why:** `report?.pdf` can decode to a name that looks exactly like
`report.pdf`; Windows-side checks (`filepath.Localize`, smbfs) catch the
device names, the zero-width characters only show in a listing.

**Context:** Found by the adversarial review of v1.0.0-rc.2. Decide whether
`isDisguisingRune` covers all of Unicode category Cf, which also affects
the names `fs.Zip` and `zipfs.Writer` reject.

**Effort:** S
**Priority:** P2
**Depends on:** None

### Check that the source can read the names before a copy

**What:** sftpfs, ftpfs and webdavfs list a name with a backslash of a
server on Unix, but reject every operation on it without
`Options.AllowBackslashInNames`.

**Why:** `CopyRecursive` or `Move` from such a server fails at that file
after the files before it were copied, because `checkSource` only checks
the names against the destination.

**Context:** Check the names of the source with the same rule in
`checkSource`. `fs.ValidateNameFileSystem` doesn't cover it: `ValidateName`
only checks the names that get written, existing entries are read with every
name, so the source needs a read-side rule of its own.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Decide whether a prefix must have a scheme

**What:** `fs.Register` accepts a prefix without `://`, like `/mnt/data`.

**Why:** After `Unregister` its URIs are local paths, like the prefixes with
a separator before `://` that `Register` refuses. The conformance suite
requires a scheme, so no file system of this repository is affected.

**Context:** Requiring `hasURIScheme` for every prefix breaks third-party
file systems with such prefixes; else document the limit on `Register`.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Keep Windows names with trailing dots in TrimExt through SubFileSystem and OverlayFileSystem

**What:** `File.TrimExt` on Windows keeps a local name that would end with
a dot or space only for the local file system itself, not through
a `SubFileSystem` or `OverlayFileSystem` that writes to the local disk.
It also keeps names with `<>"|?*`, which `isValidWindowsName` rejects, so
`C:\logs\*.log` isn't trimmed on Windows, while `/logs/*.log` is on Unix.

**Why:** `report..zip` trims to `report.` there, which Windows writes as
`report`.

**Context:** Use `localWritePath` instead of the type assertion, check only
the trailing dot and space, and document the rule.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Make CleanPath idempotent on the empty path

**What:** `LocalFileSystem.CleanPath` returns the empty path, which names no
file, for a file URI without a path or one that names no local file, but
`CleanPath("")` is `"."` like `filepath.Clean`, and `CleanPath("", "x")` is
`"x"`.

**Why:** Code outside go-fs that cleans a result again turns "no file" into
the working directory. Documented on `CleanPath`; no caller in go-fs does it.

**Context:** Returning `""` for `CleanPath("")` changes the result for
callers that pass `""` for the current directory. Decide it together with
the other file systems, whose `fsimpl.PathHelper.CleanPath("")` returns the
root, and pin the result with a conformance check.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Recognize the RFC 8089 short forms of file URIs

**What:** `File("file:/etc/hosts")`, and on Windows `file:c:/x`, are
relative local paths, because only the form `file://` is recognized.

**Why:** RFC 8089 allows these forms, and URIs from other tools can use
them. Documented in the CHANGELOG and `docs/explanation-path-and-uri-model.md`.

**Context:** Recognizing `file:` without `//` changes which strings are local
paths: a local file literally named `file:x` would become a URI. Decide
together with a parser of the whole RFC 8089 grammar.

**Effort:** S
**Priority:** P3
**Depends on:** None

### One type walk for the SubFileSystem and OverlayFileSystem path mapping

**What:** `newTreeCopier`, `writeTarget`, `isInsideDirView`, `fsReadTarget`
and `fsLinks` each switch on `*SubFileSystem` and `*OverlayFileSystem` and
reach into their private fields; "an overlay write goes to its upper layer"
is written in two of them.

**Why:** A change of the semantics of one wrapper has to be repeated in every
walk, and they drift apart.

**Context:** An unexported interface that both wrappers implement, with
`writeTarget(path)`, `readTarget(path)` and `viewTargets(path)`, keeps the
semantics next to each wrapper (the /simplify finding B3 of the v1.0 branch).

**Effort:** M
**Priority:** P3
**Depends on:** None

### State each copy and move contract once in the docs

**What:** The Windows reparse points, the "A.txt"/"a.txt" collision and other
contracts are explained in four to five doc comments (`CopyRecursive`,
`checkFileDest`, `moveByCopy`, `fs.Move`, azureblobfs `Move` and
`moveBlobs`), and the ftpfs package doc is one paragraph of about 70 lines.

**Why:** The copies drift apart with every change.

**Context:** Keep each contract on its public owner and write "see
[CopyRecursive]" elsewhere, and split the ftpfs package doc into `#`
sections (the /simplify finding B5 of the v1.0 branch).

**Effort:** S
**Priority:** P3
**Depends on:** None

### Bound the memory of the collision check of copies

**What:** When `CopyRecursive` copies into an existing local directory, it
`Lstat`s every entry it writes and keeps its whole `os.FileInfo` until the
directory is done.

**Why:** A flat directory of a million files holds about 300 MB of
`FileInfo`, and an overwrite costs up to three `Lstat`s of one path
(`checkFileDest`, `destDir.checkExisting`, `recordWrite`), and one `ReadDir`
of the directory for a match of two hard links.

**Context:** Keep a compact identity instead (device and inode on Unix,
volume and file index on Windows, behind a build-tagged helper), and let one
local `Lstat` per entry serve the destination check, `destDir.checkExisting`
and `recordWrite`.

**Effort:** S
**Priority:** P3
**Depends on:** None

### List the source of a copy to another file system only once

**What:** Before a copy to another file system, `checkSource` walks the
source with `ListDir` per directory to check every name, also of empty
directories, and `copyDir` lists every directory again while it copies.

**Why:** On object stores (s3fs, azureblobfs, dropboxfs) and over FTP or
SFTP that is twice the listing requests of the copy. The check used the
native `ListDirRecursive` before, a flat listing of about N/1000 requests
for N files on S3, N/5000 on Azure and N/2000 on Dropbox, which lists no
directories and so missed the names of empty ones. Now it sends one listing
per directory before the first byte is written: S3 lists cost more than
gets, and Dropbox rate limits (429) can fail copies of trees with many small
directories.

**Context:** Keep the listings of `checkSource` for `copyDir`, bounded in
memory, or check the names of directories with a native recursive listing
that also reports directories where a backend has one.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Compare files, not only paths, through SubFileSystems of remote file systems

**What:** The same-file and into-itself checks of copies and moves through a
`SubFileSystem` or `OverlayFileSystem` of a file system that is not local
compare paths. A symbolic link between a `SubFileSystem` and its parent, or
another name that a case-insensitive server matches to the same entry, is a
different path to the same file.

**Why:** A move between a `SubFileSystem` and its parent of sftpfs through
such a link copies the target onto itself: the writer of sftpfs truncates it
before it is read, and removing the source removes the link. Only local files
are also compared as files (`isLocalSameFile`).

**Context:** Resolve symbolic links with `Lstat`/`ReadSymbolicLink` before
comparing, or compare an identity where a backend has one (sftpfs reports no
inode). Writing to a temporary file and renaming it would also avoid the
truncation. The single-file case is done: `CopyFile` and the copy then
delete fallback of `Move` compare an existing destination with every link
resolved, also between a `SubFileSystem` and its parent, a destination
that may be another hard link or case of the source is replaced through
a new file, and moves between a `SubFileSystem` and its parent use the
native `Move` of the parent. Left are the into-itself checks of directories
(`isCopyIntoItself`, `isInsideDirView`).

**Effort:** M
**Priority:** P2
**Depends on:** None

### Don't write a moved file into the other hard links of the destination

**What:** `fs.Move` between file systems onto an existing local file with
other hard links writes into the file, so the other names get the moved
data, while `LocalFileSystem.Move` renames and leaves them.

**Why:** The two moves of the same files end differently, and the other
names of the destination change. This predates v1.0.

**Context:** Write a moved file to a temporary file in the destination
directory and rename it over the destination, which also replaces the
destination atomically, or document it. `CopyRecursive` keeps writing into
existing files like `cp`.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Check names created through `\\?\` before a copy within one file system

**What:** On Windows a copy within one file system that copies links to
directories skips `checkSource`, so names that only `\\?\` paths can create,
like `NUL` or `dot.`, fail in `copyDir` after the entries before them were
written.

**Why:** `CopyRecursive` leaves a partial copy; `Move` keeps the source.

**Context:** Only Windows CI can exercise it. Check the names in a listing
before the copy also within one file system, or only on Windows.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Cover the remaining cells of the copy, move and remove state matrices

**What:** The cycle-3 review of the v1.0 branch left about 100 untested
cells of low confidence in the state matrices of ftpfs `Remove`/`RemoveAll`,
azureblobfs `Move` and the tree copies.

**Why:** They are variants of tested outcomes, but nothing pins them.

**Context:** Redials for outcomes that are tested without one, `Close` and
cancelled contexts for paths that MLST reports as missing, copies within one
file system over local regular files, pipes and sockets, two concurrent
changes of one Azure blob during a move, and hard links within one file
system.

**Effort:** M
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

### Count the parent listing of the path of an ftpfs RemoveAll without MLST

**What:** On servers without MLST support, `RemoveAll` takes the passed path
for a directory from the `LIST` of its parent directory, which `lstat` reads
without `listChecked`, so a dropped line of the link at the passed path and
a forged directory line with its name make `RemoveAll` delete through it.

**Why:** The checks of the listings below the path, and of every parent on
servers with MLST support, fail closed; the path itself doesn't.

**Context:** Found by the documentation audit of v1.0.0-rc.4; the ftpfs
README states the gap. Use `listChecked` and the duplicate-name check of
`removeFolder` for the parent listing in `RemoveAll` on servers without
MLST, not in `Stat`, where a FIFO in the parent would fail every `Stat`.

**Effort:** S
**Priority:** P2
**Depends on:** None

### Detect links on servers with MLST support

**What:** With MLSD/MLST, `jlaffaye/ftp` reads `type=OS.unix=slink:…` as a
file and drops the target, and servers like ProFTPD report the facts of the
target, so ftpfs can't tell links from files and directories there.

**Why:** `ListDirRecursive` walks a link to a directory, and a link to an
ancestor until the server fails to resolve the path.

**Context:** `RemoveAll` deletes no files through a link on servers whose
`LIST` shows links like `ls -l`: it tries `DELE` before it deletes the
content of a directory and confirms a directory with `LIST` of the parent
over a second connection. See "Tell links from directories on Windows FTP
servers" for the servers where it can't. A real fix needs raw MLSD access,
which the client library doesn't offer.

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

### Count names of a listing in one pass

**What:** The duplicate-name check of `remover.removeFolder` calls
`listsName` for every pair of a directory and an entry of the listing.

**Why:** 30,000 directories in one listing take about 1.7 s of CPU before
the first `DELE`, 100,000 about 19 s, at every level of the tree.

**Context:** One pass into a `map[string]int`: a non-link entry counts its
`Name`, a link counts every `full[:i]` of `full = Name + " -> " + Target`
where `full[i:]` begins with `" -> "`, the names `listsName` matches.

**Effort:** S
**Priority:** P2
**Depends on:** None

### Links with " -> " in their names on servers without MLST

**What:** On servers without MLST support, `ListDir` and `ListDirRecursive`
name a link `a -> b` `a`, and `RemoveAll` of a directory that contains one
fails with the 550 of `DELE dir/a`. A link `a` to `b -> c` and a link
`a -> b` to `c` have the same `LIST` line.

**Why:** The FTP client library cuts the `LIST` line of a link at the first
` -> `. Nothing outside the tree is deleted, but the names are wrong.
Documented in the package doc and `ftpfs/README.md`.

**Context:** `lstat` and `listedItself` already match the longer names with
`listsName`. `ListDir` could report the ambiguous names, and `RemoveAll` could
retry `DELE` with the longer names that the line allows.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Separate TLS sessions for the extra FTPS connections

**What:** The LIST connection of `Remove` and `RemoveAll` and the dedicated
connection of `OpenReader` share the TLS config, and with it one session cache slot per
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
command per directory, a CWD and a LIST per parent directory and an extra
login per `RemoveAll`, plus a PWD for relative paths, an MLST of the entry
`-a` per listed directory and a redial after a broken listing connection.
The parent is listed again for every directory that is confirmed, so that
a sibling replaced with a link isn't confirmed from an earlier listing. For the removed path itself the LIST lists its
parent, which can be large: removing the N children of a directory one by
one lists it N times. A directory with subdirectories is listed twice, with
MLSD to enumerate it and with LIST to confirm its subdirectories. `Remove`
and `RemoveAll` of a path that MLST reports as missing dial the connection
too after the failed `DELE`, so a `Remove` or a cleanup `RemoveAll` of
a path that doesn't exist costs a login and a full listing of its parent,
also for an `OverlayFileSystem` with an ftpfs upper layer, which removes the
upper path of a file that is only in the base layer.

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

### Refuse case-only moves within s3fs only on case-insensitive stores

**What:** The copy then delete fallback of `fs.Move` refuses a destination
that only differs in case from the source and has its size, modification
time and permissions, because on a case-insensitive server it may be the
source. s3fs has no native `Move` and is case-sensitive, so such a move of
one key onto another existing key is refused there too.

**Why:** A rare false refusal, never data loss.

**Context:** Found by the documentation audit of v1.0.0-rc.4. Options: an
optional interface that tells case sensitivity, or skipping the refusal for
file systems known to be case-sensitive.

**Effort:** S
**Priority:** P3
**Depends on:** None

### An option to skip the directory check before writes

**What:** s3fs and azureblobfs send a listing request before every write to
check that the destination is no directory.

**Why:** It doubles the cost of small writes (a Class A request).

**Context:** Skip it when the caller just stat'ed (copies, moves), or add an
`Options` flag.

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

### Agree on the modification times of listings and Stat in s3fs

**What:** A listing reports the `LastModified` of `ListObjectsV2` with
milliseconds, `Stat` the HTTP `Last-Modified` of `HeadObject` in whole
seconds, and a listed directory (a common prefix) has a zero modification
time while `Stat` of it reports one.

**Why:** `File.StdFS` fails `testing/fstest.TestFS`, whose `DirEntry.Info`
must match `Stat`, and code that compares a listed time with a `Stat` time,
like a sync, sees every file as changed.

**Context:** Found by the conformance check `ListingHelpers/StdFS`, which
`s3fs` skips until this is decided. Options: truncate listed times to
seconds, or take the time of `Stat` from the `x-amz-meta` of the upload;
for directories, report no time in `Stat` either.

**Effort:** S
**Priority:** P2
**Depends on:** None

### Agree on the directories of listings and Stat in azureblobfs

**What:** A listed directory (a blob prefix) has a zero modification time
while `Stat` of it reports one, and a path with a backslash opens the blob
of the path with a slash, because Azure stores `\` as `/`.

**Why:** `File.StdFS` fails `testing/fstest.TestFS`: `DirEntry.Info` must
match `Stat`, and `Open("sub\\nested.txt")` must fail, a backslash is part
of an `io/fs` name.

**Context:** Found by the conformance check `ListingHelpers/StdFS`, which
`azureblobfs` skips until this is decided. For the backslash, `ValidateName`
could reject it, like the names Azure can't store.

**Effort:** S
**Priority:** P2
**Depends on:** None

## sftpfs, smbfs, webdavfs, uuiddir

### Remove the source of an sftpfs move onto another hard link

**What:** sftpfs `Move` of a file onto another hard link of it, with the
posix-rename extension, returns nil and leaves both names, because
rename(2) does nothing for two links of the same file.

**Why:** `Move` promises that the source is gone afterwards.

**Context:** A listing can't tell another hard link from the same directory
entry under another path, like through a link to its directory or another
case of its name on a case-insensitive server, which removing the source
would remove: the pre-landing review of rc.4 reproduced that data loss and
the removal was reverted. A fix needs `RealPath` of both parent directories
and a listing of that directory that shows both names as separate entries.

**Effort:** S
**Priority:** P3
**Depends on:** None

### Check trailing-dot names of existing directories on Windows SMB servers

**What:** `validateNewDirs` accepts a rejected directory name if `Stat`
finds an entry at that path. If a Windows SMB server strips trailing dots
like the Win32 layer does, `Stat("reports.")` finds `reports`, and a copy to
`reports./q3.txt` writes `reports/q3.txt`.

**Why:** The smbfs `ValidateName` rejects trailing dots because Windows
servers remove them; either the bypass is real or the smbfs doc is wrong.

**Context:** Found by the adversarial review of v1.0.0-rc.2 with a simulated
server. Check against a real Windows SMB share; Samba keeps the names.

**Effort:** S
**Priority:** P2
**Depends on:** A Windows SMB server

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
on Azurite, `SourceConditionNotMet` documented for Azure), the codes of
failed destination conditions of Copy Blob (`If-None-Match: *` on an
existing blob: 409 `BlobAlreadyExists` on Azurite; `If-Match`:
`TargetConditionNotMet` or `ConditionNotMet`), which `moveBlobs` tells apart
with a `GetProperties` of the source, the copy properties that `Move` reads
to recognize its own copy after a retried request (and that
`x-ms-copy-source` keeps a `snapshot` or `versionid` query), the answer to a
delete pinned with `If-Match` of a missing blob (412 `ConditionNotMet` on
Azurite, which `Move` checks with `GetProperties`), and the deletes of blobs
with snapshots.

Done: the azureblobfs tests run against a real storage account with
`GOFS_AZURE_TEST_CONNECTION_STRING` (a new container per run, deleted
afterwards). On a StorageV2 account without hierarchical namespace, with
blob and container soft delete, the conformance suite and every Move,
snapshot and ETag test pass with x-ms-version 2026-12-06. Azure answers a
failed source condition of Copy Blob with 412 `SourceConditionNotMet`
(also when the destination condition failed too), `If-None-Match: *` on an
existing blob with 409 `BlobAlreadyExists` like Azurite, a failed or
missing `If-Match` destination with 412 `TargetConditionNotMet`, and a
delete pinned with `If-Match` of a missing blob with 404 `BlobNotFound`
(Azurite: 412 `ConditionNotMet`), which `Move` already took as deleted.
`x-ms-copy-source` keeps the `snapshot` query of a copied snapshot. Azure
lists the snapshots of a blob before the blob, oldest first, and drops none
at a page boundary. The tests expect the answer of the server they run
against. One bug: `TestTouchKeepsMetadata` with upper-case metadata names
showed that `Touch` set them again in lower case, as the SDK lists them;
`Touch` now sets the content headers again instead.

**Effort:** S
**Priority:** P0
**Depends on:** An Azure storage account and credentials
**Completed:** 2026-11-02 (for v1.0.0)

### Don't trust the S3 paginator on a truncated page without a token

**What:** s3fs `ListDir`, `ListDirRecursive` and `RemoveAll` use the SDK
paginator, which ends silently at a truncated page without a continuation
token.

**Why:** A broken S3-compatible store can make them return an incomplete
result, or leave objects behind, without an error.

**Context:** `findObject` already checks this for `Stat` and `Remove`; a
three-line check per paginator loop.

Done: `ListDir`, `ListDirRecursive` and `RemoveAll` read the pages with `ListObjectsV2` and `continueListing`, which returns an error wrapping `errTruncatedWithoutToken` for a truncated page without a new continuation token, also for a token of an earlier page.

**Effort:** S
**Priority:** P3
**Depends on:** None
**Completed:** v1.0.0-rc.5 (2026-11-02)

### Detect hard links and case aliases before an in-place copy

**What:** `fs.CopyFile` within sftpfs or smbfs of a file onto another hard
link of it, or on a case-insensitive SMB server onto another case of its
name, still opens the destination with `O_TRUNC` and truncates the file
before it is read.

**Why:** Data loss, like for the links that the release candidate fixed.

**Context:** The core compares an existing destination with every symbolic
link resolved, but SFTP and go-smb2 report no inode numbers or link counts,
and the core can't tell a case-insensitive server. Options: a native
`CopyFile` in sftpfs and smbfs that refuses a destination with the
attributes of the source like `maybeSameAttrs` (an error, not a no-op, since
equal attributes don't prove the same file), or copying through a temporary
file in the destination directory that is renamed over the destination.

Done: `copyFileTo` writes a copy within one non-local file system without native `CopyFile` onto an existing destination with the size, modification time and permissions of the source (`isSameFileInfo`) to `gofs-copy-<random>.tmp` in its directory, closes the source and moves the new file onto the destination; a false positive only costs the extra file and move. A copy onto a symbolic link replaces the file it points to, and a temporary copy is kept and named in the error when the failed move already removed the destination. The copy then delete fallback of `Move` refuses a case-only destination with the same attributes. Found while testing: sftpfs `Move` with posix-rename onto another hard link leaves both names (see "Remove the source of an sftpfs move onto another hard link"); Samba refuses the native smbfs `Move` onto another hard link with an error and keeps both names. A move between a `SubFileSystem` and its non-local parent uses the native `Move` of the parent (not for symbolic links), and the root directory of a `SubFileSystem` is refused up front like by `SubFileSystem.Move`, after the pre-landing review of rc.4 found that the first version of that shortcut renamed the root.

**Effort:** M
**Priority:** P2
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### List without a path argument on servers without MLST that glob LIST arguments

**What:** On servers without MLST support, `remover.removeFolder`, the
`lstat` fallback, `ListDir` and `ListDirRecursive` send `LIST <path>` on the
connection of the operation. Servers that expand glob characters in that
argument (wu-ftpd, ProFTPD with `UseGlobbing` but without `mod_facts`) list
another directory for a path with `*`, `?` or `[`.

**Why:** `RemoveAll` can take a link to a directory for a directory and
delete the files it points to: `/tree/a[b]/L` linking to `/victim`, next to
`/tree/ab/L/keep.txt`, deletes `/victim/keep.txt`
(`TestRemoveAll_GlobbedLISTKnownLimitation`). `Stat` and `ListDir` describe
entries of the other directory.

**Context:** Documented in the package doc. Either send `CWD <dir>` and
`LIST` without an argument on the main connection and restore the working
directory (the login directory read with `PWD`, which relative paths resolve
against; a failed restore must mark the connection broken), about two round
trips more per listing, or use a listing connection like on MLST servers,
which fails on servers that allow one connection per user and must list
without `-a`, because the `-a` check needs MLST. Check how vsftpd (no MLST)
treats glob characters: it seems to filter by the last element only when the
literal path is no directory.

Done: `conn.list` lists a path with one of `*?[{~\\` or a leading `-` on servers without MLST after a `CWD` to its absolute path with `LIST` without an argument, then changes back to the login directory read once with `PWD`; a failed change back aborts the connection. `TestGlobbedLISTWithoutMLST` replaces the known-limitation test.

**Effort:** M
**Priority:** P2
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Redact the prefix in the errors of httpfs.New

**What:** `httpfs.New` puts the raw prefix into two error messages
(`httpfs/http.go:103` and `:106`), so the password of a prefix with user
info shows in the error.

**Why:** Every other error of go-fs redacts passwords; the README promises it
only for the invalid file system and the remote backends because of this.

**Context:** Found by the fact check of the README section of v1.0.0-rc.2.
Wrap the prefix in `fsimpl.RedactURI`. Hardening: the "into itself" errors
of `copy.go` print raw paths; use `errSubject` like the other errors.

Done: both errors redact the password, the query error also leaves out the query, and the "into itself" errors of `CopyRecursive` use `errSubject`.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Don't truncate an sftpfs or smbfs file by copying a link onto it

**What:** `fs.CopyFile` of a symbolic link on sftpfs or smbfs onto the file
it points to, or on smbfs of another case of a name onto the file, opens
the writer with `O_TRUNC` and truncates the file before it is read.

**Why:** Data loss: the same-file check of `copyFileTo` compares links,
hard links and bind mounts only for local files.

**Context:** Found by the fact check of the README section of v1.0.0-rc.2.
sftpfs and smbfs have no native `CopyFile`. Resolve the link target (or
compare file identities where the server reports them) before opening the
writer, like `isLocalSameFile` does locally.

Done: within one file system that is not local and has symbolic links, `copyFileTo` compares an existing destination with every link resolved (`fsEvalSymlinks`), also through SubFileSystems, and the copy is a no-op; the copy then delete fallback of `Move` refuses such paths as the same file. Hard links and another case of a name stay undetected, see "Detect hard links and case aliases before an in-place copy", completed too.

**Effort:** M
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Keep the source of a move into an archive writer until the archive is written

**What:** `fs.Move` into a `zipfs.Writer` or `tarfs.Writer` removes the
source as soon as its entry is in the writer's stream, but the archive is
only complete when `Writer.Close` succeeds.

**Why:** A failing `Close`, like a failed upload of the archive, or a crash
before it loses the moved files: `Move` returned nil and the source is gone.

**Context:** Found by the adversarial review of v1.0.0-rc.2 with a probe
(an archive target whose writer fails on `Close`). The copy then delete
fallback ends with `fsRemoveAll` of the source in `moveByCopy`
(`dispatch.go`), unchanged since beta.2. The zipfs and tarfs package docs
name `fs.Move` into the archive. Options: refuse `Move` into a file system
that can't read back what it wrote (both archive writers), or document
"CopyRecursive, Close, then remove the source".

Done: `Move` into a write-only file system fails with `ErrWriteOnlyFileSystem` before it copies anything; the docs name CopyRecursive, Close, then remove.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Decode Shift-JIS Unicode Path names whose second byte repeats the next letter

**What:** `zipname.Decode` ignores a valid Info-ZIP Unicode Path field when
the ASCII second byte of a double-byte character is followed by the same
letter and more non-ASCII characters, like Shift-JIS `アA日.txt`
(`83 41 41 93 FA`), and returns the CP-437 decoding instead.

**Why:** Names from Japanese Windows tools like `エリアA計画.xlsx` or
`ケースX仕様.pdf` show as mojibake. It is safe (the stored name decides),
but it is a regression of the rc.2 rule that closed the look-alike spoof.

**Context:** `keepsASCII` takes the leftmost match of a middle ASCII part.
Since a run may hold an ASCII byte only right after a byte from 0x80, that
match can be the second byte, and the next run then starts with the real
letter. Skip a match whose next byte can't start a run (from 0x80, `?` or
`_`) and add the cases above to `TestDecode` and the `FuzzKeepsASCII`
seeds. Also add rows for a dot or slash after a byte from 0x80, which pin the
`@` lower bound of the second-byte rule.

Done: a match of a middle ASCII part must be followed by a byte that can start a run, else the next match is tried.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Check the prefix of EnsureRegistered like Register

**What:** `fs.Register` refuses a prefix with a slash or backslash before
its `://`, but `fs.EnsureRegistered` registers through `registerLocked`
without that check.

**Why:** `EnsureRegistered(ctx, "backup/s3://bucket", …)` registers it, and
after the last `free` its URIs are local paths, the silent local writes the
check exists to prevent (reproduced).

**Context:** Check the prefixes in `registerLocked` or in one function that
`Register` and the creation of `EnsureRegistered` both call, and the
`prefix` argument of `EnsureRegistered` before it creates anything.

Done: `validateRegisterPrefix` runs in `registerLocked` for every prefix and alias and in `EnsureRegistered` before `create`.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Fail closed when the client library drops a LIST line

**What:** `jlaffaye/ftp` drops a line of a `LIST` listing that it can't
parse without an error, like one whose year doesn't have four digits. The
directory check of `remover.removeFolder` then can't see the line of a link,
and one forged directory line with the link's name, from a file whose name
contains a line feed, is the only entry with that name.

**Why:** A client that can write into the parent directory and set the
modification time of its link can make `RemoveAll` on a server with MLST
support delete the files that the link points to (reproduced with the test
server's `listOmits` and `listForged` hooks). Documented in the package doc.

**Context:** Count the raw lines on the data connections of the listing
connection (ftpfs dials them with its own `dialFunc`), and make the listing
an error if the parser dropped any line other than `total N`. Do the same for
the `LIST` of the operation's connection on servers without MLST support:
there a link that the server hides from `LIST`, like a dotfile on vsftpd
with `force_dot_files=NO`, or whose line the parser drops, like the first
line of a FIFO named with a line feed, doesn't count against one forged
directory line with its name, and `RemoveAll` deletes through it (reproduced
with `listOmits` and `listForged`; documented in the package doc). Add the test
case of an omitted link line with one forged line. While there, run the `-a`
check (in `remover.list`) for every listing instead of once per directory,
so it is as fresh as the listing.

Done: `conn.listChecked` counts the lines of the data connection (after TLS) and fails if the library parsed fewer entries than lines other than empty ones and "total N"; used by the listing connection and by the LIST of servers without MLST. The `-a` check runs after every listing. A link that the server hides from LIST stays a documented limit.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.4 (2026-11-01)

### Explain in the README what go-fs protects against

**What:** A section in `README.md` that explains which pitfalls and security
issues go-fs solves compared with working with string paths and the
standard library (`os`, `path/filepath`, `io/fs`), and compared with using
the client packages of a storage directly (`pkg/sftp`, `jlaffaye/ftp`,
`go-smb2`, the AWS and Azure SDKs, a WebDAV client) instead of the go-fs
file systems built on them (`sftpfs`, `ftpfs`, `smbfs`, `s3fs`,
`azureblobfs`, `webdavfs`).

**Why:** The protections are spread over the package docs, the CHANGELOG and
`docs/MIGRATION_v1.md`, so a reader of the README can't see why to use go-fs
instead of plain paths and SDK calls.

**Context:** Candidates from the v1.0 work, each with a one-line example of
the failure it prevents: a URI of an unregistered scheme is the `Invalid`
file system instead of a silent local write; RFC 8089 file URIs and the SMB
host check of Windows UNC paths; names that collide on case-insensitive or
normalizing targets fail instead of overwriting; Windows reserved names,
alternate data streams and trailing dots are rejected before anything is
written; copies don't write into pipes or devices; `RemoveAll` doesn't delete
through symbolic links (ftpfs, SubFileSystem, OverlayFileSystem); Azure moves
pinned to ETags; checked names of `CopyFile`/`Move` into directories;
redacted credentials in errors; aborted uploads that commit nothing. Link to
the reference docs for the details and the documented limits.

**Effort:** S
**Priority:** P1
**Depends on:** None
**Completed:** v1.0.0-rc.2 (2026-10-27)

### Implement ValidateName in sftpfs, ftpfs, webdavfs and tarfs

**What:** A copy or move into sftpfs, ftpfs, webdavfs or a `tarfs.Writer`
with a name that contains a backslash failed at the first such name, after
the files before it were copied.

**Why:** Moving from S3 or mem into sftpfs, ftpfs or webdavfs with a name
containing a backslash left a partial copy that a retry trips over.

**Context:** sftpfs, ftpfs, webdavfs, smbfs, s3fs, azureblobfs, dropboxfs
and the zipfs and tarfs writers implement `fs.ValidateNameFileSystem` with
their name rules, and `fstest.RunConformance` checks that every name
`ValidateName` accepts is written and listed with that name.

**Effort:** S
**Priority:** P3
**Completed:** v1.0.0-rc.2 (2026-10-26)

### Run the Windows test of SubFileSystem roots without a drive in CI

**What:** `TestNewSubFileSystem_WindowsRootWithoutDrive` skips when the
temp directory and the working directory are on different drives, which
they are on the GitHub Windows runners (`C:` and `D:`).

**Why:** The test of the Windows branch of `NewSubFileSystem` for roots like
`\x` and `/` never runs in CI, so a regression back to a root below the
working directory would pass.

**Context:** `t.Chdir(t.TempDir())` before `os.Getwd()` puts both on one
drive. Also add a file URI without a drive, `"file://" +
filepath.ToSlash(withoutDrive)`.

**Effort:** S
**Priority:** P1, before v1.0.0
**Depends on:** None
**Completed:** v1.0.0-rc.1 (2026-10-26)

### Keep Windows long-path roots of SubFileSystem unnormalized

**What:** `NewSubFileSystem` calls `filepath.Abs` on every local root on
Windows, also on an absolute long path like `\\?\C:\data\name.`, and
`GetFullPathNameW` may strip the trailing dot of a `\\?\` path.

**Why:** The view would root at the sibling `\\?\C:\data\name`; long
paths are the documented way to address such names.

**Context:** Call `filepath.Abs` only when `!filepath.IsAbs(dir)`, which
still covers `\x`, `/`, `C:` and relative roots. The first Windows CI run of
`TestLocalFileSystem_LongPathURI` shows whether `LocalFileSystem.AbsPath`
needs the same guard.

**Effort:** S
**Priority:** P2
**Depends on:** Windows CI
**Completed:** v1.0.0-rc.1 (2026-10-26)
