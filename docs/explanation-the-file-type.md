# Why File is a string

`fs.File` is one line:

```go
type File string
```

Not an interface, not a struct, not a handle. This document explains what that
buys, what it costs, and why the trade came out this way.

## The problem

A file abstraction in Go usually looks like one of these:

```go
type File interface { Read(...) ... }   // an interface
type File struct { fs FileSystem; path string }  // a struct
```

Both force every caller to construct a value before they can name a file. That
sounds harmless until you write real code:

- **You cannot declare a constant.** `const configFile = ...` is impossible for
  an interface or a struct, so paths that are genuinely compile-time constants
  become package-level `var`s, initialized in an `init` or a constructor.
- **You cannot pass a literal.** Every call site becomes
  `readFile(fs.NewFile(fs.Local, "../data.txt"))` instead of
  `readFile("../data.txt")`.
- **You have to implement marshalling.** A struct or interface needs
  `MarshalJSON`, `UnmarshalJSON`, `MarshalXML`, SQL `Scan`/`Value`, and a
  `flag.Value`, or it will not survive a round trip through a config file, a
  database column or a command line flag.
- **A struct holds a `FileSystem` reference.** Now the value's lifetime is
  entangled with the file system's, and copying it copies a reference to
  something that might be closed.

## The approach

Make the file a string, and put the file system in the string.

```go
const fileConst fs.File = "~/file.a"

var fileVar fs.File = "~/file.b"
fileVar = fileConst
fileVar = "~/file.c"
```

The value carries everything needed to find the file: either a local path, or
a URI whose prefix names a registered `FileSystem`. Methods parse the string,
look the file system up in the registry, and dispatch.

```go
fs.File("/etc/hosts").ReadAll(ctx)            // local
fs.File("s3://bucket/key.txt").ReadAll(ctx)   // s3fs, if imported and registered
```

Everything on the cost list above disappears:

- `const` works, because a string type can be constant.
- Untyped string literals convert implicitly, so `readFile("../data.txt")`
  compiles when the parameter is `fs.File`.
- Marshalling works out of the box. Any reflection-based marshaller sees
  `reflect.String` and treats it as a string, so JSON, XML, YAML, SQL drivers
  and flag parsing all handle `File` with no code in this package.
- There is no embedded file system reference, so a `File` is trivially
  copyable, comparable, and usable as a map key.

`File` still implements `fmt.Stringer`. `String()` returns `URL()`, so a local
path prints with its `file://` scheme rather than as a bare path, which makes
`%s` and `%v` output say which file system the value belongs to:

```go
f := fs.File("/etc/hosts")
f.Path()    // "/etc/hosts"
f.RawURI()  // "/etc/hosts"   the string as stored
f.URL()     // "file:///etc/hosts"
f.String()  // "file:///etc/hosts"
```

## What this costs

Three real costs, stated plainly.

**Every method call re-parses the string and hits the registry.** `ParseRawURI`
takes a read lock and scans registered prefixes longest-first. For a tight loop
over millions of files this is measurable. The mitigation is to hoist: call
`file.FileSystem()` once, or work with the `FileSystem` directly, when you are
in that regime.

**An invalid path is only detected at use time.** `fs.File("nonsense://x")`
compiles and constructs fine; you find out when a method returns an error. A
struct with a constructor could have rejected it earlier. In exchange you never
have to handle a construction error for the overwhelmingly common case of a
literal you already know is valid.

**A `File` is only meaningful while its file system is registered.** A
`File("sub://data/report.txt")` resolves to the `Invalid` file system once the
`SubFileSystem` is closed. This is the same lifetime problem the struct has,
just moved: instead of holding a dangling reference, the value goes stale.
That is the safer failure — a stale `File` errors out, it does not read or
write through a closed connection. Note which error you get, though: the
`Invalid` file system reports itself as neither readable nor writable, so the
direction gate fires before the method does. A read returns
`ErrWriteOnlyFileSystem` and a write returns `ErrReadOnlyFileSystem`, both with
the message prefix `invalid file system`. Match on `errors.Is(err, os.ErrNotExist)`
or simply on `err != nil`; do not expect `ErrInvalidFileSystem` from the high
level `File` API.

## The safety rule that falls out

Because an unrecognized string could be read as a local path, resolution has a
rule that is easy to miss and important:

> A URI that carries a scheme (contains `://`) but matches no registered file
> system resolves to the **`Invalid` file system**, not to the local one.

Without it, `fs.File("s3://bucket/key").WriteAll(ctx, data)` in a program that
forgot to import `s3fs` would silently create a local file literally named
`s3:/bucket/key`. The only scheme that maps to local is `file://`.

A path with no scheme is still treated as a local path, which is what makes
`fs.File("../data.txt")` work.

## Trade-offs

| Chosen                                            | Given up                                      |
| ------------------------------------------------- | --------------------------------------------- |
| `const` file paths, string literals at call sites | Compile-time validation of the path           |
| Zero marshalling code, works with any encoder     | A stable identity independent of the registry |
| Trivially copyable and comparable values          | Caching the resolved file system in the value |
| One type for local and remote files               | Per-call parse and registry lookup cost       |

## Alternatives considered

**A struct with an explicit `FileSystem` field.** Rejected because it loses
`const`, literals and free marshalling, and because it entangles value lifetime
with file system lifetime. Every one of those costs is paid at every call site,
while the benefit — earlier error detection — is paid back only on the rare
malformed path.

**An interface.** Rejected for the same reasons plus one more: an interface
value cannot be created from an untyped string constant at all, so
`readFile("../file.txt")` could never compile.

`FileReader` is still an interface, deliberately. It is the read-only view that
both `File` and `MemFile` implement, so a function that only reads can accept
either a real file or a name-plus-bytes held in memory. The interface is where
polymorphism is actually needed; the path type is not.

## Related

- [Paths, URIs and the registry](explanation-path-and-uri-model.md) — how the string resolves to a file system
- [The context rule](explanation-context-rule.md) — why only some methods take a `ctx`
- [Getting started](tutorial-getting-started.md)
