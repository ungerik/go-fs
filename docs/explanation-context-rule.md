# The context rule

> A method takes a `context.Context` as its first parameter **only if it can
> take long.**

That is the whole rule. This document explains what "can take long" means
here, why the alternative designs were rejected, and what the rule costs.

## The problem

go-fs has one API over the local disk and over S3, SFTP, WebDAV and Dropbox.
Those have wildly different latency profiles, and the naive responses to that
are both bad:

**Put a `ctx` on every method.** Now `file.Name()`, `file.Ext()` and
`file.Dir()` take a context. Path manipulation is pure string work that cannot
block on anything, so every one of those parameters is noise. Worse, it makes
`File` unusable in the places its string-ness is supposed to shine: you cannot
write `files[i].Name() < files[j].Name()` in a sort comparator, and you cannot
implement `fmt.Stringer`.

**Put a `ctx` on no method.** Now a `ReadAll` over a 2 GB object on a stalled
S3 connection cannot be cancelled, and a `ListDir` over a directory with a
million entries runs to completion after the caller has given up.

Both are worse than drawing a line. The question is where.

## The approach

The line is drawn at **I/O that transfers content or enumerates a remote
listing, plus dialing a connection.** Concretely:

**Takes a context:**

| Category          | Methods                                            |
| ----------------- | -------------------------------------------------- |
| Content transfer  | `ReadAll`, `ReadAllString`, `ReadAllContentHash`, `ReadJSON`, `ReadXML`, `WriteAll`, `WriteAllString`, `WriteJSON`, `WriteXML`, `Append`, `AppendString`, `Truncate`, `ContentHash` |
| Directory listing | `ListDir`, `ListDirInfo`, `ListDirRecursive`, `ListDirInfoRecursive`, `ListDirMax`, `ListDirRecursiveMax`, `ListDirIter`, `ListDirRecursiveIter`, `Glob`, `MustGlob` |
| Recursive removal | `RemoveRecursive`, `RemoveDirContents`, `RemoveDirContentsRecursive` |
| Cross-file work   | `MoveTo`, `fs.Move`, `fs.CopyFile`, `fs.CopyFileBuf`, `fs.CopyRecursive`, `fs.IdenticalFileContents`, `fs.IdenticalDirContents` |
| Dialing           | `sftpfs.Dial`, `ftpfs.Dial`, `smbfs.Dial`, `webdavfs.New`, `s3fs.NewLoadDefaultConfig`, `dropboxfs.NewAndRegister`, … |

**Does not take a context:**

| Category                        | Methods                                            |
| ------------------------------- | -------------------------------------------------- |
| Paths                           | `Name`, `Dir`, `DirAndName`, `Path`, `URL`, `RawURI`, `Ext`, `Join`, `Joinf`, `AbsPath`, `RelPathOf`, `VolumeName`, … |
| Metadata                        | `Stat`, `Info`, `Size`, `Exists`, `IsDir`, `IsHidden`, `Modified`, `Permissions`, `CheckExists`, `CheckIsDir` |
| Open                            | `OpenReader`, `OpenWriter`, `OpenAppendWriter`, `OpenReadWriter`, `OpenReadSeeker` |
| Streaming                       | `ReadFrom`, `WriteTo` — these are `io.ReaderFrom` / `io.WriterTo`, whose signatures the standard library fixes |
| Single-entry ops                | `Remove`, `MakeDir`, `MakeAllDirs`, `Rename`, `Renamef`, `Touch` |
| Ownership, links, xattrs, watch | `User`, `SetUser`, `Group`, `SetGroup`, `SetPermissions`, `CreateSymbolicLink`, `ReadSymbolicLink`, `GetXAttr`, `SetXAttr`, `Watch` |

## Why open methods don't take a context

This is the line's least obvious placement, so it deserves the argument.

`OpenReader` returns an `io.ReadCloser`. The transfer happens in the caller's
`Read` calls, not inside `Open`. A context passed to `Open` would either be
ignored after it returns — a lie — or would have to be stashed in the reader
and consulted on every `Read`, which is exactly what `io.Reader` implementations
are not supposed to do.

The honest interface is: open without a context, and cancel by closing.

```go
r, err := file.OpenReader()
if err != nil {
    return err
}
defer r.Close()

// Cancel by closing from another goroutine, or use ReadAll(ctx)
// if you want context cancellation for the whole transfer.
```

If you want context cancellation over a whole read, `ReadAll(ctx)` is the
method that offers it. That is the trade the rule makes explicit: streaming
gives you back-pressure, `ReadAll` gives you cancellation.

## Why metadata doesn't take a context

`Stat` does hit the network on a remote backend, so this is the rule's
genuinely debatable edge.

It is excluded because `Size()`, `Exists()`, `IsDir()` and `Modified()` are the
methods that make `File` pleasant, and they cannot return an error at all —
`Size()` returns `0` for a missing file, `Exists()` returns `false`. Adding a
context to methods that swallow their errors would suggest a control the caller
does not really have. A stat is also a small, bounded round trip, not a
transfer whose duration scales with file size.

When you need the error, `Stat()` and `CheckExists()` return one. When you need
cancellation around a batch of metadata calls, check `ctx.Err()` in your own
loop.

## What this costs

**A stalled `Stat` cannot be cancelled.** On an unresponsive SFTP server,
`file.Size()` blocks for the connection's timeout. Mitigation: backends set
their own dial and operation timeouts, and `sftpfs` re-dials transparently.

**You have to know which side of the line a method is on.** The rule is
learnable but not derivable from the method name alone — `Remove` takes no
context, `RemoveRecursive` does. The mnemonic that actually works: *does the
work scale with the amount of data?* Removing one entry does not; removing a
tree does.

**Two ways to read a file.** `ReadAll(ctx)` and `OpenReader()` differ in more
than convenience, and a caller who wants both streaming and cancellation has to
close the reader themselves.

## Trade-offs

| Chosen                                         | Given up                                |
| ---------------------------------------------- | --------------------------------------- |
| `Name()`, `Size()` and friends stay pure       | Cancelling a stalled metadata call      |
| `File` works in comparators and `fmt.Stringer` | A uniform "every method takes ctx" rule |
| Open returns a plain `io.ReadCloser`           | Context cancellation on streaming reads |
| Cancellable reads, writes and listings         | A single way to read a file             |

## Alternatives considered

**Context on every method.** Rejected: it breaks `fmt.Stringer`, sort
comparators and every place a `File` is used as a plain value, in exchange for
cancellation on operations that cannot block long enough to matter.

**Context-carrying `File` values** (`file.WithContext(ctx).ReadAll()`).
Rejected because it destroys the property the type exists for: a `File` that
carries a context is no longer a string, cannot be `const`, and no longer
marshals for free. See [Why File is a string](explanation-the-file-type.md).

**`*Context` twins for every method** (`ReadAll` and `ReadAllContext`).
This is what go-fs v0.x did. It doubled the API surface, and in practice the
non-context variant was the one people reached for, which meant the cancellable
path was the one nobody used. v1.0 removed the twins and gave the context to
the method that needed it. See [Migrating to v1.0](MIGRATION_v1.md).

## Related

- [Why File is a string](explanation-the-file-type.md)
- [Migrating to go-fs v1.0](MIGRATION_v1.md) — the mechanical rename from the `*Context` twins
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the same rule on the implementer side
