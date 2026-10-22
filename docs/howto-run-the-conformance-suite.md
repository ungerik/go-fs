# How to run the conformance suite

Verify that a `fs.FileSystem` implementation honours the whole go-fs contract:
metadata, paths, reading, listing, writing, every optional interface, the
overwrite semantics of moving and copying, and the error behaviour.

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
(`/a/b/conformance`) does not. The suite creates the seed tree below it, does
its own writes in a work directory next to the seed (`fstest-work`), and
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

The default seed is an empty file, a hidden file, a file whose name looks URL
escaped and two levels of sub-directories:

```
hello.txt          "Hello, World!"
empty.txt          (empty)
.hidden.txt        "hidden"
100%25 a%20b.txt   "escaped name"
sub/nested.txt     "nested content"
sub/deeper/leaf.md "# leaf"
```

The file `100%25 a%20b.txt` is named exactly like that: a backend that decodes
URL escapes in names would turn it into `100% a b.txt`, which is another file.

Any other seed works too, the suite derives the names it reads from the seed.

### 3. Adjust the config for what your file system can do

```go
fstest.RunConformance(t, myFS, fstest.Config{
    TestDir:        "/conformance",
    Seed:           fstest.FlatSeed(),  // no sub-directories
    NoDirectories:  true,               // no directory concept at all (read-only)
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
| A check fails that you can't fix right now   | `Skip`, see step 5             |

### 4. Run it

```bash
go test -run TestConformance -v ./...
```

Each contract is a named sub-test, so a failure tells you which one broke:

```
--- FAIL: TestConformance/OptionalWrite/Truncate
--- FAIL: TestConformance/MoveFileSystem/OntoDirectory
--- FAIL: TestConformance/Close/Stat
```

Re-run just that one:

```bash
go test -run 'TestConformance/OptionalWrite/Truncate' -v ./...
```

The full list of sub-tests is in the
[fstest reference](reference-fstest.md#sub-tests).

### 5. Opt out of a check temporarily

Minor versions of go-fs may add checks to the suite. When an update adds one
that your file system does not pass yet, skip it by name until it is fixed:

```go
fstest.RunConformance(t, myFS, fstest.Config{
    TestDir: "/conformance",
    Skip: func(name string) bool {
        switch name {
        case "Rename/Overwrite", "Close/Watch":
            return true
        }
        return false
    },
})
```

The names are relative to the test that called `RunConformance`, and skipping
a group like `"Close"` skips everything below it. Skipped checks show up as
`SKIP` in `go test -v` with the reason.

## Gate tests that need Docker or credentials

Repository policy: **unit tests must run offline.** A test needing a Docker
server skips when Docker is unavailable, and one needing credentials or a
public server is gated behind an environment variable.

The go-fs backends do this with the package `internal/dockertest` of the root
module: `Available` reports whether the Docker daemon answers, `SetupFailed`
fails the run when Docker is there but the server can't start, and
`ContainerName` adds a random suffix to a container name. It is internal
because it encodes the test policy of this repository; Go doesn't let
packages outside of `github.com/ungerik/go-fs/...` import it. The shape for
your own backend:

```go
func TestMain(m *testing.M) {
    // docker info fails when the CLI is missing or its daemon is not running
    if exec.Command("docker", "info").Run() != nil {
        log.Print("Docker not available, skipping Docker-based tests")
        os.Exit(m.Run())
    }
    // A unique container name and a host port that Docker picks
    // let test runs of several checkouts run at the same time.
    name := fmt.Sprintf("myfs-test-%d", os.Getpid())
    if err := startTestServer(name); err != nil {
        // Docker is there but the server is broken: fail, don't skip
        log.Printf("Docker is available but the test server could not be started: %v", err)
        os.Exit(1)
    }
    code := m.Run()
    stopTestServer(name)
    os.Exit(code)
}
```

Check for the daemon, not just the CLI: a CLI with a stopped daemon is common
on CI runners and would otherwise fail the whole run instead of skipping.

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
return exactly the error it wants to exercise. A nil function pointer never
panics: it gives a default, an emulation, or an error wrapping
`errors.ErrUnsupported`, see the [fstest reference](reference-fstest.md#mock-file-systems).

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

**`WriteSeed` fails immediately** and the suite stops after it — `TestDir` is not empty, is not writable,
or its parent directories are missing. The suite creates `TestDir` itself with
one `MakeDir`, not recursively, and it requires a missing-path `Stat` to return
an error wrapping `os.ErrNotExist`. If `TestDir` exists but is a file, the
message says so.

**`ReadSeed` fails on a read-only file system** — the contents of `TestDir` do
not match `Config.Seed` exactly. The suite compares content, not just names.

**`ListDir` fails on a file system without directories** — set
`NoDirectories: true`.

**`ReadErrors/NotExist` fails for `ListDir`** — listing a missing directory
must fail with `os.ErrNotExist`, also on object stores where directories are
only key prefixes.

**`WriteErrors/IsDirectory` or `ReadErrors/IsDirectory` fails** — reading or
writing a directory must fail with an error matching
`errors.Is(err, fs.ErrIsDirectory{})`; the error may come from opening, from
`Read`/`Write` or from `Close`, and the directory must survive.

**`OptionalWrite/Touch` fails on an existing file** — the emulation returns
`ErrUnsupported` for touching an existing file, by design. Implement
`fs.TouchFileSystem` if your backend can update a modification time.

**`OptionalWrite/Permissions` fails on bits you do not store** — set
`PermissionMask` to the bits that actually survive a round trip.

**`MoveFileSystem`, `Rename` or `MoveTo` fails** — the destination rules are in
the [fstest reference](reference-fstest.md#sub-tests): an existing file is
replaced, an existing directory is an `os.ErrExist` error and is never merged
or deleted, and a directory can't be moved into itself (`os.ErrInvalid`).

**`Canceled/...` fails** — every method with a context must return an error
that matches `errors.Is(err, context.Canceled)` for a canceled context, and
must not change anything then. Errors of client libraries that only copy the
message of the context error have to be wrapped.

**`Close/...` fails** — after `Close`, every method must return an error
wrapping `fs.ErrFileSystemClosed`, and calling `Close` again must return nil.
The sub-test names the method that didn't.

**Everything passes locally, CI fails** — CI has no Docker daemon and no
credentials. Check your `TestMain` gate and the environment variables.

## Related

- [fstest reference](reference-fstest.md) — every config field and sub-test
- [Implement a file system](tutorial-implement-a-filesystem.md) — build a backend that passes this
- [FileSystem interfaces](reference-filesystem-interfaces.md) — the contracts being verified
- [Errors](reference-errors.md) — the error contract the suite checks
