# Paths, URIs and the registry

How a `fs.File` string becomes a `FileSystem` and a path, and why resolution
works the way it does.

## The problem

`fs.File` is a string that might be `"../data.txt"`, `"/etc/hosts"`,
`"s3://bucket/key"`, `"sftp://user@host/etc/hostname"` or
`"C:\\Users\\erik\\file.txt"`. Every method has to turn that into two things:
the `FileSystem` that owns it, and a path that backend understands.

Getting this wrong is not a cosmetic bug. Resolve `"s3://bucket/key"` to the
local file system and a program that forgot an import silently writes a local
file named `s3:/bucket/key` instead of uploading. Hand a backend a path that
still carries its own prefix and every path comparison inside it breaks.

## Resolution: longest prefix wins

The registry maps a URI prefix to a `FileSystem`. `ParseRawURI` walks the
registered file systems **sorted by prefix, in reverse**, so the longest
matching prefix wins.

```
registry sorted:  file://  invalid://  s3://bucket-a  s3://bucket-b
scanned in reverse: s3://bucket-b, s3://bucket-a, invalid://, file://
lookup "s3://bucket-b/key" → first prefix that matches is s3://bucket-b
```

That ordering is what lets two S3 buckets be registered as separate file
systems. `s3fs` registers with the prefix `"s3://" + bucketName`, so the two
buckets are `s3://bucket-a` and `s3://bucket-b` — different prefixes,
different `ID()`s, and a lookup can never confuse them. If a shorter prefix
were also registered, the longer one would still win.

If no prefix matches, the *aliases* are tried. A `PrefixAliasFileSystem`
declares extra prefixes it also answers to — typically the same prefix with the
protocol's default port spelled out. The alias is rewritten to the canonical
prefix before cleaning.

## The rule that prevents silent local writes

After prefixes and aliases, resolution reaches a fork that is worth stating
explicitly:

> A URI that contains `://` but matched nothing resolves to the **`Invalid`
> file system**. A string with no scheme resolves to the **local** file system.

The only scheme that maps to local is `file://`, and that matches earlier
because `Local` is registered with it. Anything else — `s3://`, `http://`,
`sftp://` — with no backend registered is an error waiting to be reported, not
a local path.

This is the single most useful thing to know about resolution, because the
failure it prevents is silent. Without it:

```go
// s3fs never imported
fs.File("s3://bucket/key.txt").WriteAllString(ctx, data)
// would create a local file "s3:/bucket/key.txt"
```

### What the error actually looks like

The `Invalid` file system reports `ReadableWritable() = (false, false)`. The
dispatch layer checks direction before it calls the method, so the sentinel you
get is about direction, not about invalidity:

```go
f := fs.File("s3://bucket/key.txt") // s3fs not imported

_, err := f.ReadAll(ctx)
// "invalid file system: file system is write-only"
// errors.Is(err, fs.ErrWriteOnlyFileSystem) == true
// errors.Is(err, fs.ErrInvalidFileSystem)   == false

err = f.WriteAllString(ctx, "x")
// "invalid file system: file system is read-only"
// errors.Is(err, fs.ErrReadOnlyFileSystem) == true

f.Exists() // false
```

The message text names the invalid file system, which is what you see in a log.
The sentinel does not, so do not write `errors.Is(err, fs.ErrInvalidFileSystem)`
against a `File` method result and expect it to fire. Check `err != nil`, or
check the file system directly:

```go
if _, ok := f.FileSystem().(fs.InvalidFileSystem); ok {
    return fmt.Errorf("no file system registered for %s", f.RawURI())
}
```

## URL escapes: decoded for URIs, verbatim for local paths

A URI that carries a scheme has its URL escapes decoded, so
`s3://bucket/a%20b` addresses the key `a b`. A scheme-less local path is used
**verbatim**, so a local file literally named `a%20b` is reachable by that
name.

This asymmetry is deliberate. Percent-encoding is part of URI syntax and not
part of local path syntax, so decoding a local path would make some real
filenames unaddressable. If the decode fails, the original string is used
unchanged.

## Clean paths: what a backend is guaranteed

`CleanPath` is the contract boundary. Its output is what every backend method
receives, and the package guarantees a backend is never called with anything
else:

- cleaned, with `.` and `..` elements and duplicate separators removed
- using that file system's `Separator()`
- **without** the `Prefix()`
- absolute for the file system, starting with the separator or a volume

The exception is file systems whose paths start with a host name rather than a
root, like `httpfs`. Those set `PathHelper.Rooted = false`.

`Prefix() + path` is always the URI of a path. That identity is what lets
`File` round-trip: parse to `(fileSystem, path)`, and rebuild with
`fileSystem.Prefix() + path`.

A backend gets all of this for free by embedding `fsimpl.PathHelper`; see the
[fsimpl reference](reference-fsimpl.md).

## Local files have no prefix

One deliberate asymmetry: a `File` on the local file system is stored as a bare
path, not as `file:///etc/hosts`.

```go
f := fs.File("/etc/hosts")
f.Path()    // "/etc/hosts"
f.RawURI()  // "/etc/hosts"          the string as stored
f.URL()     // "file:///etc/hosts"
f.String()  // "file:///etc/hosts"   String() returns URL()
```

This is what makes `readFile("../my-local-file.txt")` work with a plain string
literal, which is the whole reason `File` is a string. `URL()` produces the
`file://` form when you need a real URI; `Path()` gives the file system path;
`RawURI()` gives the string as stored.

## Registration is reference counted

`Register` increments a count if the prefix is already registered, and
`Unregister` decrements it and only removes the file system at zero.

This exists because several callers can independently want the same connection.
`sftpfs.EnsureRegistered` builds directly on it: the first caller dials, the
rest attach to the existing connection, and each gets a `free` function. The
connection closes when the last one calls it.

```go
free, err := sftpfs.EnsureRegistered(ctx, "sftp://user@host", cred, hostKey, nil)
if err != nil {
    return err
}
defer free()
```

Registering a file system with an empty prefix panics — it could never be
resolved, so it is a programming error, not a runtime condition.

## Trade-offs

| Chosen                              | Given up                                           |
| ----------------------------------- | -------------------------------------------------- |
| Longest-prefix match                | Constant-time lookup; resolution scans the sorted registry under a read lock |
| Unmatched scheme → `Invalid`        | Being able to write a local file whose name contains `://` |
| Local files stored without a prefix | One uniform representation for all files           |
| Reference-counted registration      | Deterministic close at the first `Close` call      |
| Escapes decoded only for URIs       | A single uniform escaping rule                     |

## Alternatives considered

**Register by scheme instead of by full prefix.** Rejected because it makes two
S3 buckets, or two SFTP hosts, indistinguishable. The prefix carries the
authority, so `s3://bucket-a` and `s3://bucket-b` are genuinely different file
systems with different `ID()`s.

**Treat an unmatched URI as a local path.** Rejected: it turns a missing import
into silent data loss, which is the worst possible failure mode for a file
library.

**Store local files as `file://` URIs.** Rejected because it breaks the string
literal ergonomics that justify the `File` type in the first place.

## Related

- [Why File is a string](explanation-the-file-type.md)
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the path contract a backend implements
- [fsimpl reference](reference-fsimpl.md) — `PathHelper` implements it for you
- [Errors](reference-errors.md)
