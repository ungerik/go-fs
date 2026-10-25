# Implement a file system

You will build a working go-fs backend from scratch and watch it pass the full
conformance suite. The backend stores files in a directory of the local disk,
which keeps the storage boring so the interface work stays visible.

The point of the exercise: `FileSystem` and `WriteFileSystem` have 15 methods,
`fsimpl.PathHelper` supplies 3 of them, you write the other **12**, and you get
`Truncate`, `Append`, `RemoveAll`, `MakeAllDirs`, `CopyFile`, `Move`, `Rename`,
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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// Prefix of the URIs of dirfs file systems, followed by the id.
const Prefix = "dirfs://"

var (
	_ fs.FileSystem      = new(fileSystem)
	_ fs.WriteFileSystem = new(fileSystem)
)

type fileSystem struct {
	fsimpl.PathHelper

	id      string
	baseDir string
	closed  atomic.Bool
}
```

Those two `var _ =` lines are the most useful thing in the file. They turn "did
I implement the interface correctly?" into a compile error instead of a runtime
surprise. Add one for every optional interface you later implement.

Embedding `fsimpl.PathHelper` is what makes this short. It supplies the
`Prefix`, `Separator` and `CleanPath` methods of the interface, plus
`SplitDirAndName`, `SplitPath`, `URI`, `JoinCleanURI`, `TrimPrefix` and
`PrefixAliases`. The type is unexported, so the fields of the embedded
`PathHelper` don't become API of your package; `New` returns the
`fs.FileSystem` interface, like the backends of go-fs do.

```go
// New returns a file system storing its files below baseDir
// with the URI prefix dirfs://<id>. It is not registered.
func New(baseDir, id string) (fs.FileSystem, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{
			URIPrefix: Prefix + id,
			PathSep:   "/",
			Rooted:    true,
		},
		id:      id,
		baseDir: abs,
	}
	return f, nil
}
```

`Rooted: true` says paths are absolute and start with the separator. Set it
false only for a file system whose paths start with a host name.

Putting the id in the prefix is what lets two instances coexist: they register
as `dirfs://a` and `dirfs://b`, and longest-prefix matching keeps them apart.

`New` doesn't register the file system, like every `New` and `Dial` of go-fs.
The caller decides with `fs.Register` whether its URIs resolve through the
registry.

## Step 2: Write the metadata methods

```go
func (f *fileSystem) ID() string     { return f.id }
func (f *fileSystem) Name() string   { return "dirfs" }
func (f *fileSystem) String() string { return "dirfs at " + f.baseDir }

func (f *fileSystem) ReadableWritable() (readable, writable bool) { return true, true }

func (f *fileSystem) RootDir() fs.File { return fs.File(f.URIPrefix + "/") }

func (f *fileSystem) Close() error {
	f.closed.Store(true)
	fs.Unregister(f)
	return nil
}

func (f *fileSystem) checkClosed() error {
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	return nil
}
```

`ID()` must be **a string that identifies your file system uniquely among the
registered ones** and never changes, and it must not block. Use an identifier
the backing store gives you if it has one — a volume id, an account id —
otherwise the coordinates that identify it: a bucket name, a `user@host`, or a
generated id. Compute it at construction. The format is yours; callers are
documented not to parse it.

`ReadableWritable` is the runtime switch. Return `false` for writable and every
write returns `ErrReadOnlyFileSystem` without your code doing a single check.

`Close` unregisters the file system and makes every later call return
`fs.ErrFileSystemClosed`; calling it twice returns nil. That is why every
method below starts with `checkClosed`.

Now the one piece of real logic — mapping a file system path to a local one —
and the `File` of a path, which the typed errors carry:

```go
func (f *fileSystem) localPath(filePath string) string {
	return filepath.Join(f.baseDir, filepath.FromSlash(strings.TrimPrefix(filePath, "/")))
}

func (f *fileSystem) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}
```

You are **guaranteed** that `filePath` is a clean path: no prefix, no `..`, the
file system's separator, absolute. The package never calls you with anything
else, and it rejects an empty path with `fs.ErrEmptyPath` before it calls you.
That guarantee is why `localPath` is one line.

## Step 3: Implement reading

Three methods and you have a read-only file system:

```go
func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	info, err := os.Stat(f.localPath(filePath))
	if err != nil {
		return nil, err // already wraps os.ErrNotExist
	}
	return fs.NewFileInfo(f.file(filePath), info, strings.HasPrefix(info.Name(), ".")), nil
}

func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
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

func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, fs.NewErrIsDirectory(info.File)
	}
	file, err := os.Open(f.localPath(filePath))
	if err != nil {
		return nil, err
	}
	return file, nil
}
```

Four contract details are doing real work here:

- **Return the underlying error.** `os.Stat` and `os.Open` already produce
  errors wrapping `os.ErrNotExist`, which is exactly what the contract wants.
  Do not replace them with your own message; wrap with `%w` if you must add
  context.
- **Reading a directory is an `fs.ErrIsDirectory`.** On Unix `os.Open` opens a
  directory without an error, so `OpenReader` checks first. Callers match it
  with `errors.Is(err, fs.ErrIsDirectory{})`.
- **Check `ctx.Err()` in the listing loop.** A cancelled context must stop the
  listing and return the context error.
- **Return the callback's error unchanged.** That is how a caller stops a
  listing early.

`OpenReader` returns `nil` and not the `*os.File` together with an error,
because a nil `*os.File` in an `io.ReadCloser` is not a nil interface.

`ListDir` takes a context but `Stat` and `OpenReader` do not. That is
[the context rule](explanation-context-rule.md): listing enumerates and can run
long, a stat is a bounded round trip, and opening is not the transfer.

## Step 4: Implement writing

```go
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(
		f.localPath(filePath),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false),
	)
	if err != nil {
		if info, statErr := os.Stat(f.localPath(filePath)); statErr == nil && info.IsDir() {
			return nil, fs.NewErrIsDirectory(f.file(filePath))
		}
		return nil, err
	}
	return file, nil
}

func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	return os.Mkdir(
		f.localPath(dirPath),
		perm.OrDefault(fs.UserAndGroupReadWriteExecute).FileMode(true),
	)
}

func (f *fileSystem) Remove(filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "/" {
		return fmt.Errorf("%w: can't remove the root directory %s", os.ErrInvalid, f.file(filePath))
	}
	err := os.Remove(f.localPath(filePath))
	if err != nil {
		if entries, readErr := os.ReadDir(f.localPath(filePath)); readErr == nil && len(entries) > 0 {
			return fs.NewErrDirectoryNotEmpty(f.file(filePath))
		}
		return err
	}
	return nil
}
```

Five contract details again:

- **`OpenWriter` must truncate.** `O_TRUNC` is not optional. The `WriteAll`
  emulation relies on it so that writing fewer bytes over a larger file leaves
  no stale tail.
- **`perm` of zero means your default.** `Permissions.OrDefault` is the helper
  for exactly that.
- **`MakeDir` creates one level and errors with `os.ErrExist`** on an existing
  path. `os.Mkdir` does both.
- **The directory errors are typed.** Writing a directory is an
  `fs.ErrIsDirectory`, and removing a non-empty directory an
  `fs.ErrDirectoryNotEmpty`. The `os` package reports both with
  platform-specific errors, so the methods translate them.
- **The root is never removed.** `Remove` and `RemoveAll` of `/` return an
  error wrapping `os.ErrInvalid`, like every go-fs file system; the
  conformance suite doesn't check it.

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

The suite registers the file system for the duration of the test.

```bash
go test -run TestConformance -v ./...
```

```
--- PASS: TestConformance (0.05s)
    --- PASS: TestConformance/Metadata (0.00s)
    --- PASS: TestConformance/Paths (0.00s)
    --- PASS: TestConformance/WriteSeed (0.00s)
    --- PASS: TestConformance/ReadSeed (0.00s)
    --- PASS: TestConformance/ReadErrors (0.00s)
    --- PASS: TestConformance/ListDir (0.00s)
    --- PASS: TestConformance/FileAPI (0.01s)
    --- SKIP: TestConformance/Lstat (0.00s)
    --- PASS: TestConformance/Canceled (0.00s)
    --- PASS: TestConformance/Write (0.00s)
    --- PASS: TestConformance/LargeContent (0.00s)
    --- PASS: TestConformance/WriteErrors (0.00s)
    --- PASS: TestConformance/OptionalWrite (0.00s)
    --- SKIP: TestConformance/MoveFileSystem (0.00s)
    --- SKIP: TestConformance/CopyFileSystem (0.00s)
    --- PASS: TestConformance/Rename (0.01s)
    --- PASS: TestConformance/MoveTo (0.00s)
    --- PASS: TestConformance/CopyFile (0.00s)
    --- PASS: TestConformance/Abort (0.00s)
    --- PASS: TestConformance/HighLevelWrite (0.00s)
    --- PASS: TestConformance/Cleanup (0.02s)
    --- PASS: TestConformance/Close (0.00s)
PASS
```

The skipped groups test interfaces you didn't implement: `Lstat` needs
`fs.SymbolicLinkFileSystem`, `MoveFileSystem` and `CopyFileSystem` the native
`Move` and `CopyFile`. The sub-tests of `OptionalWrite` are skipped for the
same reason.

Now look at `Rename`, `MoveTo`, `CopyFile` and `HighLevelWrite`. You did not
implement a single optional interface, and they passed — the moves, copies,
appends and recursive removals ran on generic emulations built from the
methods you *did* write. See
[Optional interfaces and emulation](explanation-optional-interfaces.md).

## Step 6: Use it through the File API

```go
f, err := dirfs.New("/tmp/store", "demo")
if err != nil {
	log.Fatal(err)
}
defer f.Close()
err = fs.Register(f) // the URIs dirfs://demo/... resolve to f
if err != nil {
	log.Fatal(err)
}

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
var _ fs.RemoveAllFileSystem = new(fileSystem)

func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if filePath == "/" {
		return fmt.Errorf("%w: can't remove the root directory %s", os.ErrInvalid, f.file(filePath))
	}
	return os.RemoveAll(f.localPath(filePath))
}
```

Re-run the suite. `OptionalWrite/RemoveAll` now exercises your native
implementation instead of skipping, `Canceled/RemoveAll` checks that a
cancelled context changes nothing, which is why the method checks `ctx.Err()`
although `os.RemoveAll` takes no context, and `Close/RemoveAll` checks the
closed state. The native implementation must produce the same observable
result as the emulation, including "removing a path that does not exist is
not an error", which `os.RemoveAll` already satisfies.

The rule for what to implement next: **implement an optional interface only
when you can beat the emulation.** Good candidates for a real backend:

| If your backend has…           | Implement                                    |
| ------------------------------ | -------------------------------------------- |
| A server-side copy             | `CopyFileSystem`                             |
| A rename or move operation     | `MoveFileSystem` (also used by `File.Rename`) |
| A single-call recursive delete | `RemoveAllFileSystem`                        |
| A whole-object read or write   | `ReadAllFileSystem`, `WriteAllFileSystem`    |
| Random access file handles     | `ReadWriterFileSystem`, `TruncateFileSystem` |
| A cheap existence check        | `ExistsFileSystem`                           |
| Byte range reads               | See `fsimpl.RangeReader`                     |
| A recursive listing API        | `ListDirRecursiveFileSystem`                 |
| Uploads that commit on `Close` | Writers that implement `fs.WriteAborter`     |

Leave the rest alone. An unimplemented optional interface is not a gap; it is
the emulation doing its job.

## The helpers you didn't need here

A real remote backend usually reaches for these. See the
[fsimpl reference](reference-fsimpl.md):

- **`fsimpl.RangeReader`** — an `io.ReadSeekCloser` and `io.ReaderAt` for
  backends read with byte range requests. `webdavfs` and `azureblobfs` use it.
- **`fsimpl.NewWriteOnCloseFileBuffer`** — a writer for backends that can only
  write whole objects; it implements `fs.WriteAborter`.
- **`fsimpl.NewReadWriteAllSeekCloser`** — random access over whole-file
  read/write.

## What you built

A complete go-fs backend in about 170 lines that passes the conformance suite.

What you learned:

- **`FileSystem` plus `WriteFileSystem` is 15 methods, 12 of them yours.**
  Everything else is optional and emulated.
- **`fsimpl.PathHelper` handles paths.** Embed it in an unexported type, set
  `URIPrefix`, `PathSep` and `Rooted`.
- **The contract is about errors.** Wrap `os.ErrNotExist` and `os.ErrExist`,
  return `fs.ErrIsDirectory` and `fs.ErrDirectoryNotEmpty` for the directory
  cases, `fs.ErrFileSystemClosed` after `Close`, return the context error,
  honour the callback's error.
- **`New` doesn't register.** The caller registers with `fs.Register`, and
  `Close` unregisters.
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
