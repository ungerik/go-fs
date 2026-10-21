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

## Resolution: the longest prefix at a boundary wins

The registry maps a URI prefix to a `FileSystem`. `ParseRawURI` checks the
prefix of every registered file system, and the **longest prefix that
matches at a boundary** wins.

A URI matches a prefix only at a boundary: the URI ends after the prefix, the
prefix ends with a slash (like `scheme://`), or the prefix is followed by a
slash or the separator of the file system.

```
registered:          s3://logs   s3://logs-archive
"s3://logs/key"          → s3://logs           ("/" follows the prefix)
"s3://logs-archive/key"  → s3://logs-archive   (s3://logs is followed by "-")
"s3://logs"              → s3://logs           (the URI ends after the prefix)
```

That rule is what lets two S3 buckets be registered as separate file systems.
`s3fs` registers with the prefix `"s3://" + bucketName`, so the two buckets
are `s3://logs` and `s3://logs-archive` — different prefixes, different
`ID()`s, and a lookup can never confuse them, although one prefix is a string
prefix of the other. If a shorter prefix at a boundary were also registered,
like `s3://` alone, the longer one would still win.

A `PrefixAliasFileSystem` declares extra prefixes it also answers to —
typically the same prefix with the protocol's default port spelled out, like
`sftp://user@host:22`. The aliases count in the same pass as the prefixes, so
the longest match of either wins. The alias is rewritten to the canonical
prefix before cleaning.

Every file system must produce URIs that satisfy the boundary rule. A
`MemFileSystem` with a volume, for example, writes a separator between its
prefix and the volume: `mem://id/C:`.

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
fs.File("s3://bucket/key.txt").WriteAll(ctx, data)
// would create a local file "s3:/bucket/key.txt"
```

### What the error actually looks like

Every operation of the `Invalid` file system returns an error wrapping
`fs.ErrInvalidFileSystem` that names the URI, with a password in it replaced
by `xxxxx`:

```go
f := fs.File("s3://bucket/key.txt") // s3fs not registered

_, err := f.ReadAll(ctx)
// `invalid file system: no file system registered for "s3://bucket/key.txt"`
// errors.Is(err, fs.ErrInvalidFileSystem) == true
// errors.Is(err, os.ErrNotExist)          == false

err = f.WriteAllString(ctx, "x")
// the same error

exists := f.Exists() // false
```

The error does not wrap `os.ErrNotExist`: the file isn't missing, it can't be
reached at all, and treating it as missing would hide the configuration error.
Check for it before the first operation if you want a message of your own:

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
root, like the `http://` and `https://` file systems of `httpfs`. A backend
built on `fsimpl.PathHelper` sets `Rooted = false` for that.

The URI of a path is `Prefix()` followed by the path with every `%` escaped
as `%25` (`fsimpl.EscapeURIPath`). That is what lets `File` round-trip: parse
to `(fileSystem, path)`, decoding the escapes, and rebuild with the prefix and
the escaped path, so a file literally named `a%20b` keeps its name.

File systems whose paths are URIs in escaped form, like the URLs of `httpfs`
with their query strings, implement `URIFileSystem` instead. The package then
decodes nothing and lets the file system convert between URIs and paths with
`PathFromURI` and `URIFromPath`.

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

## Registration

`Register` adds a file system under its prefix and its prefix aliases.
Registering the same instance again is a no-op; a *different* instance for a
prefix that is already held is an error wrapping `fs.ErrAlreadyRegistered`, so
two parts of a program can't silently replace each other's file system.
`Unregister` removes exactly the passed instance, and `Close` of a file system
unregisters it. An empty prefix is an error, because it could never be
resolved.

Several callers can independently want the same connection. That is what
`fs.EnsureRegistered` is for: it returns the file system registered for a
prefix, or creates and registers it, and counts the references. Concurrent
calls for the same prefix share one creation, which runs without blocking the
resolution of other URIs. Every caller gets a `free` function, and the last
`free` unregisters and closes a file system that `EnsureRegistered` created; a
file system registered with plain `Register` is never closed by it.
`sftpfs.EnsureRegistered` builds directly on it:

```go
free, err := sftpfs.EnsureRegistered(ctx, "sftp://user@host", opts)
if err != nil {
    return err
}
defer free()
```

## Trade-offs

| Chosen                              | Given up                                           |
| ----------------------------------- | -------------------------------------------------- |
| Longest-prefix match                | Constant-time lookup; resolution scans the sorted registry under a read lock |
| Unmatched scheme → `Invalid`        | Being able to write a local file whose name contains `://` |
| Local files stored without a prefix | One uniform representation for all files           |
| `Register` rejects a held prefix    | Replacing a registered file system without closing it first |
| Reference counts in `EnsureRegistered` only | Sharing an instance by registering it twice |
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
