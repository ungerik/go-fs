# How to test code that uses go-fs

Replace the real file system in your tests with `fs.MemFileSystem`, so tests
run in memory: no temp directories to clean up, no disk I/O, no ordering
problems between parallel tests.

This guide is about testing *your* code. To test a file system *backend* you
wrote, see [Run the conformance suite](howto-run-the-conformance-suite.md).

## Prerequisites

- Go 1.26 or newer
- `github.com/ungerik/go-fs` in your `go.mod`
- Code that takes files as `fs.File` or `fs.FileReader` rather than opening
  paths itself

That last one is the real prerequisite. If your function hardcodes
`os.ReadFile("/etc/app.conf")` there is nothing to substitute. Take an
`fs.File` parameter and the substitution is free.

## Steps

### 1. Make the code under test take a File

```go
// Before: untestable without touching the disk
func LoadConfig() (*Config, error) {
    data, err := os.ReadFile("/etc/app/config.json")
    ...
}

// After: works against any backend
func LoadConfig(ctx context.Context, file fs.File) (*Config, error) {
    var cfg Config
    err := file.ReadJSON(ctx, &cfg)
    if err != nil {
        return nil, err
    }
    return &cfg, nil
}
```

Use `fs.FileReader` instead of `fs.File` when the function only reads. That
lets callers pass an `fs.MemFile` — a name plus bytes, no file system at all.

### 2. Create a MemFileSystem seeded with your fixtures

```go
func TestLoadConfig(t *testing.T) {
    memFS, err := fs.NewMemFileSystem(nil,
        fs.NewMemFile("config.json", []byte(`{"name":"test"}`)),
    )
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { memFS.Close() })
    err = fs.Register(memFS)
    if err != nil {
        t.Fatal(err)
    }

    cfg, err := LoadConfig(t.Context(), memFS.RootDir().Join("config.json"))
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Name != "test" {
        t.Errorf("got %q, want %q", cfg.Name, "test")
    }
}
```

The first argument is `*fs.MemFileSystemOptions`; nil means a random id and
`/` as separator. The other arguments are the initial files.

`NewMemFileSystem` doesn't register the file system. `fs.Register` makes the
`fs.File` values with its `mem://<id>` prefix resolve to it, which is what the
code under test needs. The random id isolates every instance from every other
one, so two tests running in parallel cannot see each other's files.

Always `Close()` it. Closing unregisters the file system and frees the data.

### 3. Address files through RootDir

```go
root := memFS.RootDir()          // File("mem://<id>/")
file := root.Join("config.json") // File("mem://<id>/config.json")
```

`RootDir().Join(...)` is the reliable way to build paths — it works no matter
what random id the instance got.

### 4. Build directory trees

Sub-directories are not created implicitly by a write, so make them first:

```go
dir := memFS.RootDir().Join("sub", "deeper")
err := dir.MakeAllDirs()
if err != nil {
    t.Fatal(err)
}
err = dir.Join("leaf.md").WriteAllString(t.Context(), "# leaf")
```

Or seed the whole tree up front — a `MemFile` name containing slashes is a
path:

```go
memFS, err := fs.NewMemFileSystem(nil,
    fs.NewMemFile("config.json", []byte(`{}`)),
    fs.NewMemFile("sub/deeper/leaf.md", []byte("# leaf")),
)
```

### 5. Pick the options the test needs

```go
memFS, err := fs.NewMemFileSystem(&fs.MemFileSystemOptions{
    ID:        "fixtures", // the prefix is mem://fixtures instead of a random one
    Separator: `\`,        // "/" or `\`
    Volume:    "C:",       // paths like C:\dir, URIs like mem://fixtures/C:\dir
    ReadOnly:  true,       // every write returns fs.ErrReadOnlyFileSystem
})
```

A fixed `ID` gives URIs you can write down in the test, but two instances with
the same id can't be registered at the same time: the second `fs.Register`
returns an error wrapping `fs.ErrAlreadyRegistered`. Keep it to tests that
don't run in parallel. `Separator` and `Volume` emulate Windows paths on any
platform. `ReadOnly` checks that code handles a read-only file system;
`SetReadOnly` switches it later, after the fixtures were written.

`fs.NewMemFileSystemAndRegister` creates and registers a file system in one
step. It needs an `ID`, because it returns only an error and the URIs are all
you have.

### 6. Skip the file system entirely when you only read

If the function takes `fs.FileReader`, a `MemFile` needs no file system:

```go
func TestParse(t *testing.T) {
    got, err := Parse(t.Context(), fs.NewMemFile("input.csv", []byte("a,b\n1,2\n")))
    ...
}
```

This is the cheapest option and the one to reach for first.

## Verification

```bash
go test ./... -race
```

The `-race` flag matters here: `MemFileSystem` is protected by a `sync.RWMutex`
and is safe for concurrent use, so a data race the detector reports is in your
code, not in the file system.

To confirm a test is really isolated from the disk, check that the file it
wrote is not there:

```go
if fs.File("/tmp/config.json").Exists() {
    t.Fatal("test wrote to the real file system")
}
```

## What MemFileSystem can stand in for

It implements nearly every optional interface — `Move`, `CopyFile`, `Watch`,
`Permissions`, `User`, `Group`, `ListDirRecursive`, `XAttr` and symbolic
links — so it can substitute for almost any backend, including
`LocalFileSystem`. It even implements `User`/`Group`, which the local file
system only exposes on Unix.

Limits worth knowing:

- **The file system stores copies.** `NewMemFileSystem` and `AddMemFile` copy
  the data of the `MemFile` values, and `ReadAll` returns a copy, so
  modifying a slice afterwards changes neither the file system nor fires a
  watch event. Watch events come from the operations of the file system API.
- **A `MemFile` is not copied.** `MemFile.ReadAll` returns `FileData`
  directly. Do not modify the returned slice unless you mean to modify the
  `MemFile`.
- **Writers are not `fs.WriteAborter`s.** A writer of `OpenWriter` changes the
  file while it writes, like a local file, so an aborted copy can leave
  partial content where an upload to an object store would leave nothing.

## Troubleshooting

**`invalid separator "..."` from `NewMemFileSystem`** — the `Separator` option
must be exactly `"/"` or `"\"`. It is not a root path, so passing something
like `"/tmp"` fails here.

**`invalid file system: no file system registered for "mem://…"`** — the file
system was not registered, or it was already closed. Call `fs.Register` after
`NewMemFileSystem`.

**The same error on a path you just wrote** — you probably built the path as a
string literal like `fs.File("mem://foo/config.json")` with a guessed id. Use
`memFS.RootDir().Join(...)`; the id is random per instance unless you set
`MemFileSystemOptions.ID`.

**`file does not exist` writing into a sub-directory** — create the parent
first with `MakeAllDirs`, or seed the file with a slash-separated `MemFile`
name.

**`file system prefix already registered`** — two instances with the same
`ID` are registered at the same time. Leave the `ID` empty for a random one.

**Tests interfering with each other** — each `NewMemFileSystem` is isolated, so
this means a shared instance. Create one per test, not one per package.

**Leaked registrations** — a `MemFileSystem` that is never closed stays in the
global registry for the life of the process. Always use `t.Cleanup`.

## Related

- [Getting started](tutorial-getting-started.md) — the `File` API used here
- [Run the conformance suite](howto-run-the-conformance-suite.md) — testing a backend you wrote
- [fstest reference](reference-fstest.md) — the mock file systems for testing dispatch behaviour
- [Why File is a string](explanation-the-file-type.md) — why substitution is this cheap
