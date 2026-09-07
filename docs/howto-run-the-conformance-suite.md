# How to run the conformance suite

Verify that a `fs.FileSystem` implementation honours the whole go-fs contract:
metadata, paths, reading, listing, writing, every optional interface and the
error behaviour.

Run this against a backend you wrote. To test *code that uses* go-fs, see
[Test with MemFileSystem](howto-test-with-memfilesystem.md).

## Prerequisites

- A type implementing `fs.FileSystem`, and `fs.WriteFileSystem` if it writes
- A test directory the suite may use — empty for a writable file system,
  pre-seeded for a read-only one

## Steps

### 1. Write the test

```go
package myfs

import (
    "testing"

    "github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
    myFS, err := New(...)
    if err != nil {
        t.Fatal(err)
    }

    fstest.RunConformance(t, myFS, fstest.Config{
        Name:    "My file system",
        Prefix:  "myfs://",
        TestDir: "/conformance",
    })
}
```

`TestDir` is required. `Name` and `Prefix` are checked only if you set them.

The suite registers your file system for the duration of the test and
unregisters it afterwards, because the high level `File` API resolves backends
through the global registry.

### 2. Prepare the test directory

**Writable file system:** `TestDir` must be empty. If it does not exist the
suite creates it with a single `MakeDir` — so one level below the root works
(`/conformance`), but a nested path whose parents are also missing
(`/a/b/conformance`) does not. The suite creates the seed tree below it and
removes everything at the end.

**Read-only file system:** `TestDir` must already contain exactly the seed
tree. Build it in your test setup:

```go
func TestConformance(t *testing.T) {
    seed := fstest.DefaultSeed()
    srv := startServerWith(t, seed) // your fixture

    fstest.RunConformance(t, srv.FileSystem(), fstest.Config{
        TestDir: "/conformance",
        Seed:    seed,
    })
}
```

The default seed is an empty file, a hidden file and two levels of
sub-directories:

```
hello.txt          "Hello, World!"
empty.txt          (empty)
.hidden.txt        "hidden"
sub/nested.txt     "nested content"
sub/deeper/leaf.md "# leaf"
```

### 3. Adjust the config for what your file system can do

```go
fstest.RunConformance(t, myFS, fstest.Config{
    TestDir:        "/conformance",
    Seed:           fstest.FlatSeed(),  // no sub-directories
    NoDirectories:  true,               // no directory concept at all
    SkipClose:      true,               // don't call Close at the end
    PermissionMask: fs.UserWrite,       // only these permission bits are stored
})
```

| Situation                                    | Setting                        |
| -------------------------------------------- | ------------------------------ |
| Only one directory level                     | `Seed: fstest.FlatSeed()`      |
| No directories at all, like `httpfs`         | `NoDirectories: true`          |
| The file system is shared and must stay open | `SkipClose: true`              |
| Only some permission bits survive, like SMB  | `PermissionMask: fs.UserWrite` |

### 4. Run it

```bash
go test -run TestConformance -v ./...
```

Each contract is a named sub-test, so a failure tells you which one broke:

```
--- FAIL: TestConformance/OptionalWrite/Truncate
```

Re-run just that one:

```bash
go test -run 'TestConformance/OptionalWrite' -v ./...
```

## Gate tests that need Docker or credentials

Repository policy: **unit tests must run offline.** A test needing a Docker
server skips when Docker is unavailable, and one needing credentials or a
public server is gated behind an environment variable.

```go
func TestMain(m *testing.M) {
    if !fstest.DockerAvailable() {
        log.Print("Docker not available, skipping Docker-based tests")
        os.Exit(m.Run())
    }
    if err := startTestServer(); err != nil {
        // Docker is there but the server is broken: fail, don't skip
        fstest.DockerSetupFailed("SFTP")
        os.Exit(m.Run())
    }
    defer stopTestServer()
    os.Exit(m.Run())
}
```

`DockerAvailable` checks that the CLI exists **and** the daemon answers, which
is the case that matters: a CLI with a stopped daemon is common on CI runners
and would otherwise fail the whole run instead of skipping.

`DockerSetupFailed` exits non-zero, so a broken test server never passes
silently. On Windows it only logs, because Docker there usually cannot run the
Linux images.

For credentials or a public server:

```go
func TestOnline(t *testing.T) {
    if os.Getenv("GOFS_ONLINE_TESTS") != "1" {
        t.Skip("set GOFS_ONLINE_TESTS=1 to run tests against the public server")
    }
    ...
}
```

## Test the dispatch layer with the mocks

To check how your code behaves against a minimal backend versus a
fully-featured one:

```go
mock := &fstest.MockFileSystem{}              // required interfaces only → emulation
full := &fstest.MockFullyFeaturedFileSystem{} // every optional interface → native
```

Both use per-method function pointers, so a test can make a single method
return exactly the error it wants to exercise.

## Verification

Run the whole workspace the way CI does:

```bash
./test-workspace.sh
```

That vets, race-tests, `staticcheck`s and `gosec`s every module in `go.work`,
which is what the repository's own backends have to pass.

For one module:

```bash
go test -race -count=1 ./...
```

`-count=1` defeats the test cache, which matters when the result depends on an
external server.

## Troubleshooting

**`Config.TestDir must not be empty`** — set `TestDir`. There is no default.

**`a writable file system must implement fs.WriteFileSystem`** —
`ReadableWritable` reports `writable == true` but the type does not implement
`OpenWriter`, `MakeDir` and `Remove`. Fix one or the other.

**`WriteSeed` fails immediately** — `TestDir` is not empty, is not writable,
or its parent directories are missing. The suite creates `TestDir` itself with
one `MakeDir`, not recursively, and it requires a missing-path `Stat` to return
an error wrapping `os.ErrNotExist`. If `TestDir` exists but is a file, the
message says so.

**`ReadSeed` fails on a read-only file system** — the contents of `TestDir` do
not match `Config.Seed` exactly. The suite compares content, not just names.

**`ListDir` fails on a file system without directories** — set
`NoDirectories: true`.

**`OptionalWrite/Touch` fails on an existing file** — the emulation returns
`ErrUnsupported` for touching an existing file, by design. Implement
`fs.TouchFileSystem` if your backend can update a modification time.

**`SetPermissions` fails on bits you do not store** — set `PermissionMask` to
the bits that actually survive a round trip.

**`Close` sub-test fails** — after `Close`, every method must return an error
wrapping `fs.ErrFileSystemClosed`, and calling `Close` again must return nil.

**Everything passes locally, CI fails** — CI has no Docker daemon and no
credentials. Check your `TestMain` gate and the environment variables.

## Related

- [fstest reference](reference-fstest.md) — every config field and sub-test
- [Implement a file system](tutorial-implement-a-filesystem.md) — build a backend that passes this
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the contracts being verified
- [Errors](reference-errors.md) — the error contract the suite checks
