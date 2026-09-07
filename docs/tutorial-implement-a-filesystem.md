# Implement a file system

You will build a working go-fs backend from scratch and watch it pass the full
conformance suite. The backend stores files in a directory of the local disk,
which keeps the storage boring so the interface work stays visible.

The point of the exercise: you implement **12 methods**, and you get `Truncate`,
`Append`, `RemoveAll`, `MakeAllDirs`, `CopyFile`, `Move`, `Rename`,
`ListDirRecursive`, random access and the whole `File` API for free.

## What you'll need

- Go 1.26 or newer
- Familiarity with the `fs.File` API — do
  [Getting started](tutorial-getting-started.md) first if not

## Step 1: Set up the module

```bash
mkdir dirfs && cd dirfs
go mod init example.com/dirfs
go get github.com/ungerik/go-fs
```

Create `dirfs.go` with the type and the compile-time checks that state what it
promises to be:

```go
// Package dirfs implements a go-fs FileSystem that stores its files
// in a directory of the local file system.
package dirfs

// The complete import block for the finished file; you add the uses
// as you go through the steps.
import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const Prefix = "dirfs://"

var (
	_ fs.FileSystem      = new(FileSystem)
	_ fs.WriteFileSystem = new(FileSystem)
)

type FileSystem struct {
	fsimpl.PathHelper

	id      string
	baseDir string
}
```

Those two `var _ =` lines are the most useful thing in the file. They turn "did
I implement the interface correctly?" into a compile error instead of a runtime
surprise. Add one for every optional interface you later implement.

Embedding `fsimpl.PathHelper` is what makes this short. It supplies `Prefix`,
`Separator`, `CleanPath`, `SplitDirAndName`, `SplitPath`, `IsAbsPath`,
`AbsPath`, `IsHidden`, `URL`, `JoinCleanURI` and `TrimPrefix` — every
path-related method of the interface.

```go
// New returns a FileSystem storing files below baseDir
// and registers it under the prefix dirfs://<id>.
func New(baseDir, id string) (*FileSystem, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	f := &FileSystem{
		PathHelper: fsimpl.PathHelper{
			URIPrefix: Prefix + id,
			PathSep:   "/",
			Rooted:    true,
		},
		id:      id,
		baseDir: abs,
	}
	fs.Register(f)
	return f, nil
}
```

`Rooted: true` says paths are absolute and start with the separator. Set it
false only for a file system whose paths start with a host name, like `httpfs`.

Putting the id in the prefix is what lets two instances coexist: they register
as `dirfs://a` and `dirfs://b`, and longest-prefix matching keeps them apart.

## Step 2: Write the metadata methods

```go
func (f *FileSystem) ID() string     { return f.id }
func (f *FileSystem) Name() string   { return "dirfs" }
func (f *FileSystem) String() string { return "dirfs at " + f.baseDir }

func (f *FileSystem) ReadableWritable() (readable, writable bool) { return true, true }

func (f *FileSystem) RootDir() fs.File { return fs.File(f.URIPrefix + "/") }

func (f *FileSystem) Close() error {
	fs.Unregister(f)
	return nil
}
```

`ID()` must be a **stable identifier of the backing store** that never blocks —
a volume id, a bucket name, a `user@host`. Compute it at construction.

`ReadableWritable` is the runtime switch. Return `false` for writable and every
write returns `ErrReadOnlyFileSystem` without your code doing a single check.

Now the one piece of real logic — mapping a file system path to a local one:

```go
func (f *FileSystem) localPath(filePath string) string {
	return filepath.Join(f.baseDir, filepath.FromSlash(strings.TrimPrefix(filePath, "/")))
}
```

You are **guaranteed** that `filePath` is a clean path: no prefix, no `..`, the
file system's separator, absolute. The package never calls you with anything
else. That guarantee is why this function is one line.

## Step 3: Implement reading — and run the suite

Three methods and you have a read-only file system:

```go
func (f *FileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	info, err := os.Stat(f.localPath(filePath))
	if err != nil {
		return nil, err // already wraps os.ErrNotExist
	}
	file := fs.File(f.JoinCleanURI(filePath))
	return fs.NewFileInfo(file, info, strings.HasPrefix(info.Name(), ".")), nil
}

func (f *FileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	entries, err := os.ReadDir(f.localPath(dirPath))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := fs.File(f.JoinCleanURI(dirPath, entry.Name()))
		err = callback(fs.NewFileInfo(file, info, strings.HasPrefix(entry.Name(), ".")))
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *FileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return os.Open(f.localPath(filePath))
}
```

Three contract details are doing real work here:

- **Return the underlying error.** `os.Stat` and `os.Open` already produce
  errors wrapping `os.ErrNotExist`, which is exactly what the contract wants.
  Do not replace them with your own message; wrap with `%w` if you must add
  context.
- **Check `ctx.Err()` in the listing loop.** A cancelled context must stop the
  listing and return the context error.
- **Return the callback's error unchanged.** That is how a caller stops a
  listing early.

`ListDir` takes a context but `Stat` and `OpenReader` do not. That is
[the context rule](explanation-context-rule.md): listing enumerates and can run
long, a stat is a bounded round trip, and opening is not the transfer.

## Step 4: Implement writing

```go
func (f *FileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return os.OpenFile(
		f.localPath(filePath),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false),
	)
}

func (f *FileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return os.Mkdir(
		f.localPath(dirPath),
		perm.OrDefault(fs.UserAndGroupReadWriteExecute).FileMode(true),
	)
}

func (f *FileSystem) Remove(filePath string) error {
	return os.Remove(f.localPath(filePath))
}
```

Three contract details again:

- **`OpenWriter` must truncate.** `O_TRUNC` is not optional. The `WriteAll`
  emulation relies on it so that writing fewer bytes over a larger file leaves
  no stale tail.
- **`perm` of zero means your default.** `Permissions.OrDefault` is the helper
  for exactly that.
- **`MakeDir` creates one level and errors with `os.ErrExist`** on an existing
  path. `os.Mkdir` does both. `Remove` handles a file or an *empty* directory
  and errors on a non-empty one — `os.Remove` again matches.

That is all 12 methods. Run `go build ./...` — it should compile clean.

## Step 5: Run the conformance suite

Create `dirfs_test.go`:

```go
package dirfs

import (
	"testing"

	"github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
	f, err := New(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	fstest.RunConformance(t, f, fstest.Config{
		Name:    "dirfs",
		Prefix:  "dirfs://test",
		TestDir: "/conformance",
	})
}
```

```bash
go test -run TestConformance -v ./...
```

```
--- PASS: TestConformance (0.01s)
    --- PASS: TestConformance/Metadata (0.00s)
    --- PASS: TestConformance/Paths (0.00s)
    --- PASS: TestConformance/WriteSeed (0.00s)
    --- PASS: TestConformance/ReadSeed (0.00s)
    --- PASS: TestConformance/ReadErrors (0.00s)
    --- PASS: TestConformance/ListDir (0.00s)
    --- PASS: TestConformance/FileAPI (0.00s)
    --- PASS: TestConformance/Write (0.00s)
    --- PASS: TestConformance/WriteErrors (0.00s)
    --- PASS: TestConformance/OptionalWrite (0.00s)
    --- PASS: TestConformance/HighLevelWrite (0.00s)
    --- PASS: TestConformance/Cleanup (0.00s)
    --- PASS: TestConformance/Close (0.00s)
PASS
```

Look at `OptionalWrite`. You did not implement a single optional interface, and
it passed — because every one of them has a generic emulation built from the
methods you *did* write. See
[Optional interfaces and emulation](explanation-optional-interfaces.md).

## Step 6: Use it through the File API

```go
f, err := dirfs.New("/tmp/store", "demo")
if err != nil {
	log.Fatal(err)
}
defer f.Close()

err = fs.File("dirfs://demo/notes/todo.txt").Dir().MakeAllDirs() // emulated
if err != nil {
	log.Fatal(err)
}
err = fs.File("dirfs://demo/notes/todo.txt").WriteAllString(ctx, "buy milk")
if err != nil {
	log.Fatal(err)
}
err = fs.File("dirfs://demo/notes/todo.txt").Append(ctx, []byte("\nand eggs")) // emulated
if err != nil {
	log.Fatal(err)
}
err = fs.File("dirfs://demo/notes").RemoveRecursive(ctx) // emulated
```

`MakeAllDirs`, `Append` and `RemoveRecursive` all work. None of them exist in
your code.

## Step 7: Add an optional interface, but only when it pays

Now measure before you write. `RemoveAll` is emulated as a recursive listing
plus one `Remove` per entry. The local file system can do it in a single call,
so this one pays:

```go
var _ fs.RemoveAllFileSystem = new(FileSystem)

func (f *FileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return os.RemoveAll(f.localPath(filePath))
}
```

Re-run the suite. `OptionalWrite` now exercises your native implementation
instead of the emulation, and it must produce the same observable result —
including "removing a path that does not exist is not an error", which
`os.RemoveAll` already satisfies.

The rule for what to implement next: **implement an optional interface only
when you can beat the emulation.** Good candidates for a real backend:

| If your backend has…           | Implement                                    |
| ------------------------------ | -------------------------------------------- |
| A server-side copy             | `CopyFileSystem`                             |
| A rename or move operation     | `MoveFileSystem`, `RenameFileSystem`         |
| A single-call recursive delete | `RemoveAllFileSystem`                        |
| A whole-object read or write   | `ReadAllFileSystem`, `WriteAllFileSystem`    |
| Random access file handles     | `ReadWriterFileSystem`, `TruncateFileSystem` |
| A cheap existence check        | `ExistsFileSystem`                           |
| Byte range reads               | See `fsimpl.RangeReader`                     |
| A recursive listing API        | `ListDirRecursiveFileSystem`                 |

Leave the rest alone. An unimplemented optional interface is not a gap; it is
the emulation doing its job.

## The helpers you didn't need here

A real remote backend usually reaches for these. See the
[fsimpl reference](reference-fsimpl.md):

- **`fsimpl.RangeReader`** — an `io.ReadSeekCloser` for backends read with byte
  range requests. `webdavfs` and `azureblobfs` use it.
- **`fsimpl.NewWriteOnCloseFileBuffer`** — an append writer for backends that
  can only write whole objects.
- **`fsimpl.NewReadWriteAllSeekCloser`** — random access over whole-file
  read/write.
- **`fsimpl.NewDirTree`** — a directory index for archives that have none.
  `tarfs` uses it.

## What you built

A complete go-fs backend in about 120 lines that passes all 13 conformance
sub-tests.

What you learned:

- **`FileSystem` plus `WriteFileSystem` is 12 methods.** Everything else is
  optional and emulated.
- **`fsimpl.PathHelper` handles paths.** Embed it, set `URIPrefix`, `PathSep`
  and `Rooted`.
- **The contract is about errors.** Wrap `os.ErrNotExist` and `os.ErrExist`,
  return the context error, honour the callback's error. Returning the
  standard library's errors unchanged usually satisfies it.
- **`var _ fs.XFileSystem = new(T)`** turns interface mistakes into compile
  errors.
- **`fstest.RunConformance` is the specification** — if it passes, your backend
  behaves like every other one.

## Next steps

- [FileSystem interfaces](reference-filesystem-interfaces.md) — every interface and its exact fallback
- [Run the conformance suite](howto-run-the-conformance-suite.md) — read-only backends, Docker gating, config options
- [fsimpl reference](reference-fsimpl.md) — the implementer helpers
- [Optional interfaces and emulation](explanation-optional-interfaces.md) — why this design works
- Read a real backend: `webdavfs` is standard library only and a good size to
  study
