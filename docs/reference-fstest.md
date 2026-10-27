# fstest

```go
import "github.com/ungerik/go-fs/fstest"
```

The conformance suite that every go-fs backend must pass, plus the mock file
systems the repository's own tests use. The package only depends on the
standard library and go-fs itself.

Point `RunConformance` at your `fs.FileSystem` and it verifies the whole
contract: metadata, paths, reading, listing, writing, every optional
interface, the overwrite semantics of moving and copying, and the error
behaviour. It checks content, not just existence.

For a task-oriented walk-through see
[Run the conformance suite](howto-run-the-conformance-suite.md).

## Compatibility

The API of the package is stable, the suite is not frozen: minor versions of
go-fs may add checks when the contract of `fs.FileSystem` is clarified or a gap
in the suite is found, so a file system that passed may fail after an update.
`Config.Skip` opts out of a new check temporarily, until the file system is
fixed.

## RunConformance

```go
func RunConformance(t *testing.T, fileSystem fs.FileSystem, cfg Config)
```

```go
func TestMyFS(t *testing.T) {
    fstest.RunConformance(t, myFS, fstest.Config{
        Name:    "My file system",
        Prefix:  "myfs://",
        TestDir: "/conformance",
    })
}
```

`RunConformance` registers the file system for the duration of the test if it
is not registered already, and unregisters it again in a `t.Cleanup`. It needs
that because the high level `File` API resolves a `FileSystem` through the
global registry.

A writable file system must implement `fs.WriteFileSystem`; the suite fails
immediately if `ReadableWritable` reports `writable == true` and it does not.

## Config

```go
type Config struct {
    Name           string
    Prefix         string
    TestDir        string
    Seed           Seed
    NoDirectories  bool
    SkipClose      bool
    PermissionMask fs.Permissions
    Skip           func(testName string) bool
}
```

| Field            | Meaning                                            |
| ---------------- | -------------------------------------------------- |
| `Name`           | Expected result of `FileSystem.Name`. Not checked if empty. |
| `Prefix`         | Expected result of `FileSystem.Prefix`. Not checked if empty. |
| `TestDir`        | **Required.** File system path, without prefix, that the suite works in. For a writable file system it must be empty and either exist or have an existing parent; the suite creates the seed and its own files below it and removes them again. For a read-only file system it must already contain exactly the seed tree. |
| `Seed`           | The tree the suite expects or creates. `DefaultSeed()` if nil. |
| `NoDirectories`  | Set for read-only file systems without a directory concept (`httpfs`): `Stat` of `TestDir`, directory listings and the directory error checks are skipped. The write checks need directories. |
| `SkipClose`      | Keeps the suite from calling `Close` at the end.   |
| `PermissionMask` | The permission bits the file system actually stores. `SetPermissions` is only checked for these bits. Zero means all bits. SMB shares keep only a read-only flag, so their mask is `fs.UserWrite`. |
| `Skip`           | Called with the name of every sub-test before it runs, relative to the test that called `RunConformance` (`"Close"`, `"Rename/Overwrite"`). Returning true skips the sub-test and everything below it. Nil runs everything. |

An empty `TestDir` fails the test immediately.

```go
fstest.RunConformance(t, myFS, fstest.Config{
    TestDir: "/conformance",
    // Until the next release fixes the overwrite semantics of Rename
    Skip: func(name string) bool { return name == "Rename/Overwrite" },
})
```

## Seed

```go
type Seed map[string][]byte
```

Keys are slash separated paths relative to `TestDir`, values are the file
contents. Directories are implied by the paths.

```go
func DefaultSeed() Seed // used when Config.Seed is nil
```

```go
Seed{
    "hello.txt":          []byte("Hello, World!"),
    "empty.txt":          nil,
    ".hidden.txt":        []byte("hidden"),
    "100%25 a%20b.txt":   []byte("escaped name"),
    "sub/nested.txt":     []byte("nested content"),
    "sub/deeper/leaf.md": []byte("# leaf"),
}
```

An empty file, a hidden file, a file whose name looks percent-encoded (it must
keep that name, `100% a b.txt` would be another file) and two levels of
sub-directories, which is what exercises the listing and recursion paths.

```go
func FlatSeed() Seed // DefaultSeed without the sub directories
```

Use `FlatSeed` for file systems that only support a single directory level.

You can also pass your own:

```go
fstest.RunConformance(t, myFS, fstest.Config{
    TestDir: "/conformance",
    Seed: fstest.Seed{
        "a.txt":     []byte("a"),
        "dir/b.txt": []byte("b"),
    },
})
```

The suite derives every name it reads from the seed, so any tree works,
including an empty one. The checks that need an existing file use the first
file by name with content in the least deep directory, the relative path
checks use the first file of the deepest directory. The write checks create
their own files in a work directory below `TestDir`, `fstest-work` or, if the
seed already has a top level entry with that name, `fstest-work-1` and so on.

## Sub-tests

Every check is a named sub-test, so `go test -run` can select one,
`Config.Skip` can skip one, and a failure names the contract that broke.

| Sub-test         | Runs when                  | Checks                                             |
| ---------------- | -------------------------- | -------------------------------------------------- |
| `Metadata`       | always                     | `ID`, `Prefix`, `Name`, `String`, `Separator`, `ReadableWritable`, `RootDir` |
| `Paths`          | always                     | `CleanPath`, the derived path methods, escaped names, prefix aliases, and that URIs match the prefix at a boundary and round-trip through `fs.ParseRawURI` |
| `WriteSeed`      | writable                   | Creates the seed tree                              |
| `ReadSeed`       | readable                   | Reads the seed through the `FileSystem` methods    |
| `ReadErrors`     | readable                   | `NotExist`: `os.ErrNotExist` from `Stat`, `OpenReader`, `ReadAll` and `ListDir` of a missing path; `ListFile`: listing a file; `IsDirectory`: `fs.ErrIsDirectory` for reading a directory |
| `ListDir`        | readable, `!NoDirectories` | Listing, patterns, the callback error, `ListDirMax`, `ListDirRecursive`, the URIs of the listed files |
| `FileAPI`        | readable                   | The reads through the high level `File` API, `iotest.TestReader` and reads past the end on `OpenReadSeeker` |
| `Lstat`          | readable, `fs.SymbolicLinkFileSystem` | `Lstat` of the seed and of a missing path |
| `Canceled`       | readable                   | One sub-test per method with a context (`ListDir`, `ReadAll`, `File.ReadAll`, ..., and for writable file systems `WriteAll`, `Move`, `File.Rename`, `fs.CopyFile`, ...): a canceled context returns an error matching `context.Canceled` and changes nothing |
| `WriteOnly`      | not readable               | Reads return `ErrWriteOnlyFileSystem`              |
| `Write`          | readable + writable        | Core write methods                                 |
| `LargeContent`   | readable + writable        | Content larger than 1 MiB: `OpenReader`, `ReadAll`, `OpenReadSeeker` (`ReadAt` and `Seek`), `PastEOF`, `CopyFile` |
| `WriteErrors`    | readable + writable        | `MakeDirExisting` (`os.ErrExist`), `RemoveMissing` (`os.ErrNotExist`), `RemoveNonEmptyDir` (`fs.ErrDirectoryNotEmpty`), `IsDirectory` (`fs.ErrIsDirectory` for writing a directory), `MissingParent` |
| `OptionalWrite`  | readable + writable        | One sub-test per optional write interface the file system implements natively (`WriteAll`, `Append`, `OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `Touch`, `Exists`, `MakeAllDirs`, `RemoveAll`, `SymbolicLink`, `XAttr`, `Permissions`); the others are skipped; their emulations are covered by `Write` (`OpenReadWriter`) and `HighLevelWrite`, and `SymbolicLink`, `XAttr` and `Permissions` have no emulation. `SymbolicLinkToDirectory` runs if links to directories work: the native `ListDirRecursive` and `File.ListDirInfoRecursive` list such a link as an entry with `IsSymlink` set and `IsDir` false and not the files of its directory again, and a listing that starts at the link lists its directory |
| `MoveFileSystem` | readable + writable, `fs.MoveFileSystem` | The overwrite semantics of the native `Move`: `Self`, `ToMissing`, `Overwrite`, `OntoDirectory`, `DirectoryOntoFile`, `IntoItself` |
| `CopyFileSystem` | readable + writable, `fs.CopyFileSystem` | The overwrite semantics of the native `CopyFile`: `Self`, `ToMissing`, `Overwrite`, `OntoDirectory` |
| `Rename`         | readable + writable        | `File.Rename`, native or emulated: `Self`, `ToMissing`, `Overwrite`, `OntoDirectory`, `DirectoryOntoFile`, `InvalidName` |
| `ValidateName`   | writable `fs.ValidateNameFileSystem` | `ValidateName` accepts names that every file system writes and wraps `os.ErrInvalid` for the others; `Rejected`: `fs.CopyFile` to such a name fails with `os.ErrInvalid` before it writes anything; `Accepted` (readable only): every name it accepts is written, read back and listed with that name |
| `MoveTo`         | readable + writable        | `File.MoveTo`: `Self`, `Overwrite`, `IntoDirectory`, `DirectoryOntoFile`, `IntoItself` |
| `CopyFile`       | readable + writable        | `fs.CopyFile`: `Self`, `Overwrite`, `IntoDirectory`, `MissingParents` |
| `Abort`          | readable + writable        | Writers that implement `fs.WriteAborter` (`OpenWriter`, `OpenAppendWriter`, `File.OpenAppendWriter`, each with `NewFile` and `ExistingFile`): `Abort` leaves nothing behind, `Close` after `Abort` returns nil |
| `HighLevelWrite` | readable + writable        | Writes through the `File` API, including the copy to and move to another file system |
| `Cleanup`        | readable + writable        | Removes everything the suite created               |
| `ReadOnly`       | not writable               | Writes return `ErrReadOnlyFileSystem`              |
| `Close`          | `!SkipClose`               | `Close` twice, unregistering, then one sub-test per method (`Close/Stat`, `Close/OpenWriter`, ...) that must return `ErrFileSystemClosed` |

A write-only file system gets `WriteOnly` instead of the read group; a
read-only one gets `ReadOnly` instead of the write group.

The overwrite semantics checked by `MoveFileSystem`, `CopyFileSystem`,
`Rename` and `MoveTo`:

| Destination        | Move / Rename                                     | CopyFile            |
| ------------------ | ------------------------------------------------- | ------------------- |
| missing            | proceeds                                          | proceeds            |
| existing file      | replaced (a directory source: `fs.ErrIsNotDirectory`) | replaced        |
| existing directory | error wrapping `os.ErrExist`, never merged or deleted | `fs.ErrIsDirectory` |

A destination equal to the source is a no-op, and moving a directory into
itself or one of its descendants is an error wrapping `os.ErrInvalid`.
`File.MoveTo` and `fs.CopyFile` resolve an existing directory destination to
"into the directory" before these rules apply.

`fs.Local` can't be closed, so the operations after `Close` are not checked
for it. Every other file system must report `ErrFileSystemClosed`; one that
can't be closed sets `SkipClose`. An operation that doesn't return within ten
seconds after `Close` is reported instead of blocking the suite, and so is a
move of a directory into itself that doesn't return within ten seconds.

## Mock file systems

```go
type MockFileSystem struct{ ... }
type MockFullyFeaturedFileSystem struct{ ... }
```

Both implement the go-fs interfaces with **per-method function pointers**, so a
test controls individual behaviours without a real backing store: set a field
to return exactly the error or value the test needs, leave it nil for the
default. No method panics for a nil field.

`MockFileSystem` implements `fs.FileSystem` and `fs.WriteFileSystem` and
nothing else, so it is the file system to test the *emulation* paths against:
every optional operation takes the fallback. With nil fields `Prefix` is
`MockPrefix` or `mock://`, `ID` is the prefix, `Name` and `String` are
`MockFileSystem`, `Separator` is `/`, `ReadableWritable` is `true, true`, and
`RootDir` and `CleanPath` follow the prefix. `Stat`, `ListDir`, `OpenReader`,
`OpenWriter`, `MakeDir` and `Remove` return an error wrapping
`errors.ErrUnsupported`, because there is nothing to emulate them with. `Close`
always unregisters the file system and returns the result of `MockClose`.

`MockFullyFeaturedFileSystem` implements every optional interface, so it is the
one to test that dispatch prefers the native implementation over the fallback.
A nil field falls back to an emulation built from the other methods, like the
package's own emulations: `Exists`, `ReadAll`, `WriteAll`, `Append`,
`OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `MakeAllDirs`, `RemoveAll`,
`CopyFile`, `Move` (with the overwrite semantics above) and `ListDirRecursive`
use the primitives, `Touch` creates a missing file, `Lstat` is `Stat`,
`IsHidden` is the dot rule, `PrefixAliases` is empty, `VolumeName` is empty,
`IsAbsPath`/`AbsPath`/`RelPath` treat every path as absolute and
`ValidateName` accepts every name. `Watch`,
`SetPermissions`, `User`, `SetUser`, `Group`, `SetGroup`, `CreateSymbolicLink`,
`ReadSymbolicLink` and the extended attribute methods return an error wrapping
`errors.ErrUnsupported`, because the package has no emulation for them either.

`MockFullyFeaturedFileSystem` deliberately does not implement
`fs.URIFileSystem`: a file system that does maps its URIs to paths itself and
the package stops decoding percent-encoded characters, which would give the mock another path
model than every other file system. Embed the mock in a type with the three
methods of `fs.URIFileSystem` to test that model.

Between the two they cover both sides of every branch in the dispatch layer.
See [Optional interfaces and emulation](explanation-optional-interfaces.md).

## Related

- [Run the conformance suite](howto-run-the-conformance-suite.md) — how to wire it up
- [Implement a file system](tutorial-implement-a-filesystem.md) — build a backend and pass the suite
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the contracts being verified
- [Test with MemFileSystem](howto-test-with-memfilesystem.md) — testing *code that uses* go-fs, rather than a backend
