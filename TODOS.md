# TODOS

Deferred findings of the pre-landing review of the v1.0.0 pre-release
(2026-10-25). None of them changes the v1.0 API, so they can land in a
v1.x release. The roadmap's "Not in v1.0.0" list in `docs/V1_ROADMAP.md`
has the earlier ones.

## ftpfs

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

### RemoveAll sends DELE for every directory on MLST servers

**What:** On servers with MLST support `removeFolder` tries DELE for every
directory before it deletes the content, one failing command per directory.

**Why:** It is how a link that MLST describes as a directory gets deleted
itself, but it adds a round trip per directory of a large tree.

**Context:** The trade-off is documented in the package doc. Try DELE first
only for entries whose listing suggests a link, once the client library
exposes the needed MLSD facts, or only at the top level of `RemoveAll`.

**Effort:** M
**Priority:** P4
**Depends on:** MLSD facts like `unix.mode` or the link target in `jlaffaye/ftp`

## azureblobfs

### Bound the memory and parallelize the requests of a directory Move

**What:** The native `Move` of a directory keeps the name of every blob in
memory and copies and deletes one blob per sequential request.

**Why:** A tree of millions of blobs holds every name until all copies and
deletes finish, and N blobs take 2N round trips one after another.

**Context:** `RemoveAll` caps its name list at `maxRemoveAllNames` and lists
again beyond it; `Move` could do the same, copying page by page from a
second listing and deleting from a third. Copies can run with a small worker
pool, deletes with the blob batch API (256 per batch) where the credential
supports it. The roadmap has the batch API for `RemoveAll` already.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Back off while polling a pending server-side copy

**What:** `copyBlob` polls `GetProperties` every 100 ms while a copy is
pending, without backoff and without a limit other than ctx.

**Why:** A slow server-side copy costs about 10 paid requests per second
per blob, and nothing stops it under a context without a deadline. The
native `Move` now calls `copyBlob` once per blob.

**Context:** Back off exponentially from 100 ms to a few seconds, honour
`Retry-After`, and optionally give up after a configurable maximum wait.
The loop predates the v1.0 work.

**Effort:** S
**Priority:** P3
**Depends on:** None

## fs (core)

### Check destination names before a copy or move between file systems

**What:** A copy or move into a file system that rejects some names fails
at the first such name, after the files before it were copied.

**Why:** Moving from S3 or mem into sftpfs, ftpfs or webdavfs with a name
containing a backslash leaves a partial copy that a retry trips over.

**Context:** `moveByCopy` already checks the source for links to directories
before it writes anything (`checkDirLinks`). An optional interface like
`ValidName(name string) error` implemented by the backends with name rules
would let that pre-flight reject such names too; adding it is not breaking.
The partial copy is documented in `docs/MIGRATION_v1.md`.

**Effort:** M
**Priority:** P3
**Depends on:** None

### Reject Unicode look-alikes of the backslash on Windows servers

**What:** `fsimpl.CheckNoBackslash` rejects only U+005C.

**Why:** Windows servers that convert names to an ANSI code page with
best-fit mapping turn U+FF3C, U+FE68, U+2216 and, under CP932, U+00A5 into
a backslash, so such a name could still be split at a separator. The review
rated this low confidence (3 of 10).

**Context:** Either reject the common best-fit equivalents unless
`AllowBackslashInNames` is set, or document the residual case at the option
of sftpfs, ftpfs and webdavfs.

**Effort:** S
**Priority:** P4
**Depends on:** None

## Completed
