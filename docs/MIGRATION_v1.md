# Migrating to go-fs v1.0

v1.0 changes the consumer-facing `File`, `FileReader` and `MemFile` API in a
mechanical way, and redesigns the implementer-facing `FileSystem` interface.
Nobody outside this repository implements `FileSystem`, so this guide covers
the consumer side; implementers read `docs/V1_ROADMAP.md`.

## The context rule

A method takes `ctx context.Context` as its first parameter only if it is a
potentially long-running read or write operation: content transfer,
directory iteration, and connect functions. Metadata and open methods do
not. The `*Context` twins are gone; where a pair existed, the ctx variant
survives under the short name.

| v0.1                                   | v1.0                                         |
| -------------------------------------- | -------------------------------------------- |
| `f.ReadAll()` / `f.ReadAllContext(ctx)` | `f.ReadAll(ctx)`                             |
| `f.ReadAllString()` / `...Context(ctx)` | `f.ReadAllString(ctx)`                       |
| `f.ContentHash()` / `...Context(ctx)`   | `f.ContentHash(ctx)`                         |
| `f.WriteAll(data)` / `...Context(ctx, data)` | `f.WriteAll(ctx, data)`                 |
| `f.WriteAllString(s)` / `...Context`    | `f.WriteAllString(ctx, s)`                   |
| `f.ListDir(cb)` / `f.ListDirContext(ctx, cb)` | `f.ListDir(ctx, cb)`                   |
| `f.ListDirIter(...)` / `...Context`     | `f.ListDirIter(ctx, ...)`                    |
| `f.ListDirInfo(cb)` / `...Context`      | `f.ListDirInfo(ctx, cb)`                     |
| `f.ListDirRecursive(cb)` / `...Context` | `f.ListDirRecursive(ctx, cb)`                |
| `f.ListDirRecursiveIter(...)`           | `f.ListDirRecursiveIter(ctx, ...)`           |
| `f.ListDirInfoRecursive(cb)`            | `f.ListDirInfoRecursive(ctx, cb)`            |
| `f.ListDirMax(n)` / `...Context(ctx, n)` | `f.ListDirMax(ctx, n)`                      |
| `f.ListDirRecursiveMax(n)`              | `f.ListDirRecursiveMax(ctx, n)`              |
| `f.Glob(p)`, `f.MustGlob(p)`, `fs.Glob(p)`, `fs.MustGlob(p)` | same with `ctx` first |
| `f.Truncate(size)`                      | `f.Truncate(ctx, size)`                      |
| `f.MoveTo(dest)`                        | `f.MoveTo(ctx, dest)`                        |
| `f.RemoveRecursive()` / `...Context`    | `f.RemoveRecursive(ctx)`                     |
| `f.RemoveDirContentsRecursive()`        | `f.RemoveDirContentsRecursive(ctx)`          |
| `f.RemoveDirContents(patterns...)`      | `f.RemoveDirContents(ctx, patterns...)`      |
| `fs.TempFileCopy(src)`                  | `fs.TempFileCopy(ctx, src)`                  |
| `uuiddir.Remove(base, id)`              | `uuiddir.Remove(ctx, base, id)`              |
| `uuiddir.RemoveDir(base, dir)`          | `uuiddir.RemoveDir(ctx, base, dir)`          |

Unchanged (no ctx): `Stat`, `Info`, `Exists`, `CheckExists`, `IsDir`,
`CheckIsDir`, `Size`, `Modified`, `Permissions`, `Open*`, `Touch`, `MakeDir`,
`MakeAllDirs`, `Remove`, `Rename`, `Renamef`, `User`/`Group`, `*XAttr`,
symbolic links, `Watch`, `IsEmptyDir`, `IsReadable`, `IsWritable`,
`IsHidden`, all path methods. `Append`, `AppendString`, `ReadJSON`, `WriteJSON`,
`ReadXML`, `WriteXML`, `ReadAllContentHash`, `fs.Move`, `fs.CopyFile`,
`fs.CopyRecursive`, `fs.ReadMemFile`, `fs.Zip`, `fs.UnzipToMemFiles` already
took a context.

`MemFile` implements the same `FileReader` interface, so `MemFile.ReadAll`,
`ReadAllString` and `ContentHash` take a context too.

## Removed

- `File.ListDirChan` / `File.ListDirRecursiveChan`: use `ListDirIter` or
  `ListDir` with a callback.
- `File.GobEncode` / `File.GobDecode`: encoding a `File` with `encoding/gob`
  used to read the whole file. `MemFile` still round-trips through gob;
  use `fs.ReadMemFile(ctx, file)` to get one.
- `fs.MemDir`: a `MemFile` with a `FileName` ending in `/` is a directory.
- `fs.FileInfoCache`: it was only used by dropboxfs and is internal there now.
- `fs.ReadOnlyBase`, `fs.FullyFeaturedFileSystem`, `fs.RelPathFileSystem`,
  `fs.RunFileSystemTests`, the `tests` package: implementer-facing, see the
  roadmap.

## Behaviour changes

- `File.Touch` on a file system without native touch support creates a
  missing file and returns `ErrUnsupported` for an existing one instead of
  truncating it.
- `File.RemoveRecursive` does not fail for a missing path.
- `File.IsWritable` is also true for an existing writable directory.
- `File.MoveTo` with an existing destination directory moves into it (as
  before); backends now receive the final path.
- `File.IsSymbolicLink` is `false` on file systems without symbolic link
  support instead of calling the backend.
- A `File` with a scheme is URL-decoded once when parsed; a scheme-less local
  path is used verbatim, so a local file literally named `a%20b` is reachable.
- `fs.MakeTempDir` uses `os.MkdirTemp`; the new `fs.CreateTempFile` creates a
  temporary file atomically, `fs.TempFile` still only returns a path.
- Error strings and types are unchanged, but every file system now satisfies
  `errors.Is(err, os.ErrNotExist)` / `os.ErrExist` / `fs.ErrReadOnlyFileSystem`
  / `fs.ErrWriteOnlyFileSystem` / `fs.ErrFileSystemClosed` consistently.

## Mechanical migration

Most call sites can be rewritten with `gofmt -r` or sed. The rewrites below
assume a `ctx` variable is in scope; in tests use `t.Context()` (but
`context.Background()` inside `t.Cleanup`, where the test context is already
cancelled):

```sh
# renamed *Context twins
for m in ReadAll ReadAllString ContentHash WriteAll WriteAllString \
         ListDir ListDirIter ListDirInfo ListDirRecursive ListDirRecursiveIter \
         ListDirInfoRecursive ListDirMax ListDirRecursiveMax RemoveRecursive \
         RemoveDirContentsRecursive RemoveDirContents; do
  find . -name '*.go' -exec sed -i '' "s/\.${m}Context(/.${m}(/g" {} +
done

# context-less variants that gained a ctx parameter
find . -name '*.go' -exec sed -i '' \
  -e 's/\.ReadAll()/.ReadAll(ctx)/g' \
  -e 's/\.ReadAllString()/.ReadAllString(ctx)/g' \
  -e 's/\.ContentHash()/.ContentHash(ctx)/g' \
  -e 's/\.RemoveRecursive()/.RemoveRecursive(ctx)/g' \
  -e 's/\.RemoveDirContentsRecursive()/.RemoveDirContentsRecursive(ctx)/g' \
  -e 's/\.ListDir(func/.ListDir(ctx, func/g' \
  -e 's/\.ListDirInfo(func/.ListDirInfo(ctx, func/g' \
  -e 's/\.ListDirRecursive(func/.ListDirRecursive(ctx, func/g' \
  {} +
```

`WriteAll`, `WriteAllString`, `ListDirMax`, `ListDirIter`, `Glob`, `Truncate`,
`MoveTo` and `RemoveDirContents` take other arguments before, so their
call sites are easiest to fix from the compiler errors: insert `ctx, ` as the
first argument. Callers of `FileSystem` implementation methods with the same
names (`fileSystem.WriteAll(ctx, path, ...)`) already pass a context and must
not be rewritten.
