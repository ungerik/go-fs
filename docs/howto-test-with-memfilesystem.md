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
    memFS, err := fs.NewMemFileSystem("/",
        fs.NewMemFile("config.json", []byte(`{"name":"test"}`)),
    )
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { memFS.Close() })

    cfg, err := LoadConfig(t.Context(), memFS.RootDir().Join("config.json"))
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Name != "test" {
        t.Errorf("got %q, want %q", cfg.Name, "test")
    }
}
```

The first argument is the path separator and must be `"/"` or `"\"`; anything
else is an error.

`NewMemFileSystem` **registers itself** in the global registry and gets a random
`mem://<id>` prefix, so every instance is isolated from every other one. Two
tests running in parallel cannot see each other's files.

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
memFS, err := fs.NewMemFileSystem("/",
    fs.NewMemFile("config.json", []byte(`{}`)),
    fs.NewMemFile("sub/deeper/leaf.md", []byte("# leaf")),
)
```

### 5. For a single file, use the one-shot constructor

When a test needs exactly one file, this skips the path building:

```go
memFS, file, err := fs.NewSingleMemFileSystem(fs.NewMemFile("a.txt", []byte("a")))
if err != nil {
    t.Fatal(err)
}
t.Cleanup(func() { memFS.Close() })

// file is already the ready-to-use fs.File
data, err := file.ReadAllString(t.Context())
```

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

It implements nearly every optional interface — `Rename`, `Move`, `Watch`,
`Permissions`, `User`, `Group`, `ListDirMax`, `ListDirRecursive`, `XAttr` and
symbolic links — so it can substitute for almost any backend, including
`LocalFileSystem`. It even implements `User`/`Group`, which the local file
system only exposes on Unix.

Two limits worth knowing:

- **Watch events are synthesized from mutations that go through the file
  system API.** If you obtain a `MemFile.FileData` byte slice outside the API
  and modify it in place, no watch event fires.
- **`ReadAll` returns `FileData` directly, without copying.** That is a
  deliberate performance choice. Do not modify the returned slice unless you
  mean to modify the file.

## Troubleshooting

**`invalid separator "..."` from `NewMemFileSystem`** — the first argument is
the path separator and must be exactly `"/"` or `"\"`. It is not a root path,
so passing something like `"/tmp"` fails here.

**`file does not exist` on a path you just wrote** — you probably built the
path as a string literal like `fs.File("mem://foo/config.json")` with a guessed
id. Use `memFS.RootDir().Join(...)`; the id is random per instance.

**`file does not exist` writing into a sub-directory** — create the parent
first with `MakeAllDirs`, or seed the file with a slash-separated `MemFile`
name.

**Tests interfering with each other** — each `NewMemFileSystem` is isolated, so
this means a shared instance. Create one per test, not one per package.

**Leaked registrations** — a `MemFileSystem` that is never closed stays in the
global registry for the life of the process. Always use `t.Cleanup`.

## Related

- [Getting started](tutorial-getting-started.md) — the `File` API used here
- [Run the conformance suite](howto-run-the-conformance-suite.md) — testing a backend you wrote
- [fstest reference](reference-fstest.md) — the mock file systems for testing dispatch behaviour
- [Why File is a string](explanation-the-file-type.md) — why substitution is this cheap
