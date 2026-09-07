# fstest

```go
import "github.com/ungerik/go-fs/fstest"
```

The conformance suite every go-fs backend passes, plus the mock file systems
and the Docker gate the repository's own tests use.

Point `RunConformance` at your `fs.FileSystem` and it verifies the whole
contract: metadata, paths, reading, listing, writing, every optional
interface, and the error behaviour. It checks content, not just existence.

For a task-oriented walk-through see
[Run the conformance suite](howto-run-the-conformance-suite.md).

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
}
```

| Field            | Meaning                                            |
| ---------------- | -------------------------------------------------- |
| `Name`           | Expected result of `FileSystem.Name`. Not checked if empty. |
| `Prefix`         | Expected result of `FileSystem.Prefix`. Not checked if empty. |
| `TestDir`        | **Required.** File system path, without prefix, that the suite works in. For a writable file system it must exist and be empty; the suite creates the seed below it and removes it again. For a read-only file system it must already contain exactly the seed tree. |
| `Seed`           | The tree the suite expects or creates. `DefaultSeed()` if nil. |
| `NoDirectories`  | Set for file systems without a directory concept (`httpfs`): `Stat` of `TestDir` and directory listings are not checked. |
| `SkipClose`      | Keeps the suite from calling `Close` at the end.   |
| `PermissionMask` | The permission bits the file system actually stores. `SetPermissions` is only checked for these bits. Zero means all bits. SMB shares keep only a read-only flag, so their mask is `fs.UserWrite`. |

An empty `TestDir` fails the test immediately.

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
    "sub/nested.txt":     []byte("nested content"),
    "sub/deeper/leaf.md": []byte("# leaf"),
}
```

An empty file, a hidden file and two levels of sub-directories, which is what
exercises the listing and recursion paths.

```go
func FlatSeed() Seed // no sub directories
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

## Sub-tests

The suite runs these as named sub-tests, so `go test -run` can select one and
a failure names the contract that broke.

| Sub-test         | Runs when                  | Checks                                             |
| ---------------- | -------------------------- | -------------------------------------------------- |
| `Metadata`       | always                     | `ID`, `Prefix`, `Name`, `String`, `Separator`, `ReadableWritable`, `RootDir` |
| `Paths`          | always                     | `CleanPath` and the derived path methods           |
| `WriteSeed`      | writable                   | Creates the seed tree                              |
| `ReadSeed`       | readable                   | Reads the seed through the `FileSystem` methods    |
| `ReadErrors`     | readable                   | `os.ErrNotExist` and friends on reads              |
| `ListDir`        | readable, `!NoDirectories` | Listing, patterns, recursion                       |
| `FileAPI`        | readable                   | The same reads through the high level `File` API   |
| `WriteOnly`      | not readable               | Reads return `ErrWriteOnlyFileSystem`              |
| `Write`          | readable + writable        | Core write methods                                 |
| `WriteErrors`    | readable + writable        | `os.ErrExist` on `MakeDir`, and the rest           |
| `OptionalWrite`  | readable + writable        | Every optional write interface, native or emulated |
| `HighLevelWrite` | readable + writable        | Writes through the `File` API                      |
| `Cleanup`        | readable + writable        | Removes everything the suite created               |
| `ReadOnly`       | not writable               | Writes return `ErrReadOnlyFileSystem`              |
| `Close`          | `!SkipClose`               | `Close`, then `ErrFileSystemClosed` afterwards     |

A write-only file system gets `WriteOnly` instead of the read group; a
read-only one gets `ReadOnly` instead of the write group.

## Docker gating

Repository policy: unit tests run offline. Tests that need Docker skip when
Docker is unavailable, but must **not** skip when Docker is there and the test
server is simply broken.

```go
func DockerAvailable() bool
func DockerSetupFailed(server string)
```

`DockerAvailable` reports whether Docker can actually run containers: the CLI
is installed *and* its daemon answers. A CLI with no reachable daemon is the
common case on CI runners and on machines with Docker Desktop stopped, so
treating it as "installed" would fail the whole run instead of skipping.

`DockerSetupFailed` is what a backend's `TestMain` calls when Docker is
available but the server could not be built or started. It logs and exits
non-zero, so a broken test server never passes silently. On Windows it only
logs and returns, because Docker there usually cannot run the Linux images.

```go
func TestMain(m *testing.M) {
    if !fstest.DockerAvailable() {
        log.Print("Docker not available, skipping sftp tests")
        os.Exit(m.Run())
    }
    if err := startTestServer(); err != nil {
        fstest.DockerSetupFailed("sftp")
        os.Exit(m.Run())
    }
    defer stopTestServer()
    os.Exit(m.Run())
}
```

## Mock file systems

```go
type MockFileSystem struct{ ... }
type MockFullyFeaturedFileSystem struct{ ... }
```

Both implement the go-fs interfaces with **per-method function pointers**, so a
test controls individual behaviours without a real backing store: leave a field
nil for the default, set it to return exactly the error or value the test needs.

`MockFileSystem` implements `fs.FileSystem` and `fs.WriteFileSystem` and
nothing else, so it is the file system to test the *emulation* paths against:
every optional operation takes the fallback.

`MockFullyFeaturedFileSystem` implements every optional interface, so it is the
one to test that dispatch prefers the native implementation over the fallback.

Between the two they cover both sides of every branch in the dispatch layer.
See [Optional interfaces and emulation](explanation-optional-interfaces.md).

## Related

- [Run the conformance suite](howto-run-the-conformance-suite.md) — how to wire it up
- [Implement a file system](tutorial-implement-a-filesystem.md) — build a backend and pass the suite
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the contracts being verified
- [Test with MemFileSystem](howto-test-with-memfilesystem.md) — testing *code that uses* go-fs, rather than a backend
