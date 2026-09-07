# Optional interfaces and emulation

go-fs defines 28 file system interfaces. A backend implements two of them and
gets the other 26 for free. This document explains the mechanism and why it is
built this way.

## The problem

Backends differ enormously in what they can do natively.

SFTP has real random access file handles, so `Truncate` is one call. S3 has
none: an object is written whole, so truncating means download, cut, upload.
Dropbox can hash server-side. WebDAV has a `COPY` verb. HTTP can do nothing but
`GET`.

Two obvious designs both fail:

**One fat interface every backend implements.** Every backend has to write the
download-cut-upload dance for `Truncate` itself, and every backend writes it
slightly differently. Bugs multiply by the number of backends. Adding a method
to the interface breaks every backend at once, including ones outside this
repository.

**A minimal interface and nothing else.** Now the caller writes the
download-cut-upload dance, at every call site, and `File.Truncate` cannot exist.
The uniform `File` API — the reason the library exists — evaporates.

## The approach

Split the difference by capability, not by backend.

- **`FileSystem`** is the irreducible core: identity, paths, `Stat`, `ListDir`,
  `OpenReader`, `Close`.
- **`WriteFileSystem`** adds `OpenWriter`, `MakeDir`, `Remove`.
- Everything else is a **small optional interface** — usually one method —
  paired with a **generic emulation** built from the core primitives.

The dispatch layer (`dispatch.go`) sits between the `File` methods and the
backend. For each operation it type-asserts for the optional interface, uses it
if present, and runs the emulation if not.

```go
func fsTruncate(ctx context.Context, fileSystem FileSystem, filePath string, size int64) error {
    w, err := fsWritable(fileSystem)
    if err != nil {
        return err
    }
    if t, ok := w.(TruncateFileSystem); ok {
        return t.Truncate(filePath, size) // native
    }
    // emulation: stat, read, cut or zero-pad, write back
    ...
}
```

The caller sees one behaviour. The backend author writes one method only when
they can beat the emulation.

```
File.Truncate(ctx, 1024)
        │
        ▼
   dispatch.go ──── implements TruncateFileSystem? ──yes──► backend.Truncate
        │                                                   (sftp: one call)
        no
        ▼
   emulation: Stat → ReadAll → cut/pad → WriteAll
   (s3: download, modify, upload — correct, just not cheap)
```

## Why every emulation is built on the primitives

The emulations call back through the dispatch layer, not directly into the
backend. `fsAppend` falls back to `fsReadAll` + `fsWriteAll`, and each of those
checks for its *own* optional interface first.

That composition matters. A backend that implements `ReadAllFileSystem` and
`WriteAllFileSystem` but not `AppendFileSystem` gets an append built from its
own fast read and fast write, not from a generic `OpenReader`/`OpenWriter`
loop. Capabilities compose instead of being all-or-nothing.

It also means adding one optional interface to a backend speeds up every
emulation layered on top of it.

## The three fallback shapes

Every optional interface resolves to one of three outcomes, and knowing which
tells you what to expect from a backend that skips it.

**1. Transparent.** Same result, possibly slower. `Exists` becomes a `Stat`.
`ListDirMax` becomes a `ListDir` that stops early. `CopyFile` becomes a
streamed read-write. Most interfaces are here.

**2. Buffered.** Same result, but the whole file passes through memory.
`OpenReadWriter` becomes `fsimpl.ReadWriteAllSeekCloser`, which reads the file
on first use and writes it back on `Close`. `OpenAppendWriter` becomes an
`fsimpl.FileBuffer` seeked to the end. `Truncate` becomes read-modify-write.

This is where the cost is real and worth knowing: random access on an S3 object
buffers the whole object. Correct, and fine for a config file; not fine for a
50 GB archive.

**3. Refused.** Some things cannot be emulated at all, so they return
`ErrUnsupported` wrapping `errors.ErrUnsupported`: `SetPermissions`, `User`,
`Group`, symbolic links, extended attributes, `Watch`.

`Touch` is the interesting hybrid. The emulation creates a *missing* file, but
returns `ErrUnsupported` for an existing one, because "update the modification
time" cannot be faked without rewriting the file, and silently rewriting a file
the caller only wanted to touch would be a nasty surprise.

## Why not just check a capability flag?

An alternative is one interface plus `Supports(op) bool`. Rejected for two
reasons.

Type assertion is checked by the compiler. If a backend declares
`func (f *myFS) Truncate(path string, size int64) error` with the wrong
signature, it simply does not satisfy `TruncateFileSystem` and takes the
emulation — no silent misbehaviour. With a flag, the method would be found by
name or not at all, and a signature mismatch becomes a runtime surprise.

Interfaces also make the capability set open. A backend outside this repository
can implement any subset without this package knowing about it, and adding a
new optional interface here does not break a single existing implementation.
That last property is what made the v1.0 interface reshuffle possible at all.

## How this is verified

Two mocks in `fstest` cover both sides of every branch:

- `MockFileSystem` implements the required interfaces and nothing else, so
  every operation against it exercises the emulation.
- `MockFullyFeaturedFileSystem` implements every optional interface, so every
  operation against it exercises the native path.

`fstest.RunConformance` then runs the same contract against real backends, and
because it tests through both the `FileSystem` methods and the high level
`File` API, it catches a native implementation that disagrees with the
emulation it replaced. The backend support matrix in
[FileSystem interfaces](reference-filesystem-interfaces.md) is the output of
that suite, not a hand-maintained list.

## Trade-offs

| Chosen                                    | Given up                                      |
| ----------------------------------------- | --------------------------------------------- |
| 26 small interfaces                       | A single interface you can read in one screen |
| Backends implement only what they improve | Knowing a backend's cost from its type        |
| Emulation means every op works everywhere | Failing loudly when an op will be slow        |
| Type assertion per operation              | Zero-cost dispatch                            |
| Open capability set                       | A closed, exhaustively documented set         |

The second row is the one that bites: `File.OpenReadWriter` on an S3 object
compiles, runs, and quietly buffers 50 GB. The support matrix exists to make
that predictable, and this is the honest cost of a uniform API over
non-uniform backends.

## Alternatives considered

**Return `ErrUnsupported` instead of emulating.** Rejected because it pushes
the emulation to every caller, which is where it will be written worst and most
often. The library writes it once, correctly, and the support matrix documents
where it is expensive.

**Per-backend wrapper types** (`s3fs.WithRandomAccess(fs)`). Rejected: it makes
the capability visible in the type, which is good, but it means callers must
know their backend at compile time — the opposite of what a `File` string is
for.

## Related

- [FileSystem interfaces](reference-filesystem-interfaces.md) — every interface and its exact fallback
- [Implement a file system](tutorial-implement-a-filesystem.md) — decide what to implement
- [fsimpl reference](reference-fsimpl.md) — the buffers the emulations use
- [fstest reference](reference-fstest.md) — the mocks and the conformance suite
