# Getting started with go-fs

You will build a small program that reads and writes files, then point the
exact same code at three different backends — the local disk, an in-memory file
system and an HTTP server — without changing the part that does the work.

By the end you will understand the one idea the library is built on: a file is
a string, and the string says which file system it belongs to.

## What you'll need

- Go 1.26 or newer (`go version`)
- A terminal and a directory to work in
- Nothing else — the core library has four dependencies and no services to set
  up

## Step 1: Write and read a file

Create a directory and initialise a module:

```bash
mkdir gofs-tutorial && cd gofs-tutorial
go mod init example.com/gofs-tutorial
go get github.com/ungerik/go-fs
```

Create `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	fs "github.com/ungerik/go-fs"
)

func main() {
	ctx := context.Background()

	file := fs.File("hello.txt")

	err := file.WriteAllString(ctx, "Hello, go-fs!")
	if err != nil {
		log.Fatal(err)
	}

	text, err := file.ReadAllString(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(text)
	fmt.Println("name:", file.Name(), "size:", file.Size())
}
```

Run it:

```bash
go run .
```

```
Hello, go-fs!
name: hello.txt size: 13
```

You just wrote and read a file. Two things already happened that are worth
naming.

`fs.File("hello.txt")` is a **conversion, not a constructor**. `File` is
defined as `type File string`, so an untyped string literal becomes one for
free. There is no error to handle and nothing to close.

`WriteAllString` takes a `ctx`, `Name()` and `Size()` do not. That is the
library's [context rule](explanation-context-rule.md): a method takes a context
only if it can take long. Writing content can; reading a name cannot.

## Step 2: Point the same code at a different file system

Change nothing about how the file is used — only where it lives.

Replace the body of `main` with this:

```go
func main() {
	ctx := context.Background()

	// An in-memory file system, registered under a random mem:// prefix
	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		log.Fatal(err)
	}
	defer memFS.Close()

	for _, file := range []fs.File{
		"hello.txt",                       // local disk
		memFS.RootDir().Join("hello.txt"), // memory
	} {
		err := file.WriteAllString(ctx, "Hello, go-fs!")
		if err != nil {
			log.Fatal(err)
		}
		text, err := file.ReadAllString(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-40s %s\n", file.URL(), text)
	}
}
```

```bash
go run .
```

```
file:///path/to/gofs-tutorial/hello.txt  Hello, go-fs!
mem://Xk3p9QwLm2nRtY7vZbCd/hello.txt     Hello, go-fs!
```

Same `WriteAllString`, same `ReadAllString`, two completely different storage
backends. The URI prefix decided which one ran.

`NewMemFileSystem` registers itself under a random `mem://<id>` prefix, so
every instance is isolated. That is what makes it a drop-in substitute in tests
— see [Test with MemFileSystem](howto-test-with-memfilesystem.md).

## Step 3: Read from an HTTP server

Add a blank import at the top of the file:

```go
import (
	"context"
	"fmt"
	"log"

	fs "github.com/ungerik/go-fs"
	_ "github.com/ungerik/go-fs/httpfs"
)
```

```bash
go get github.com/ungerik/go-fs/httpfs
```

Then add this at the end of `main`:

```go
	data, err := fs.File("https://example.com/").ReadAll(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("fetched", len(data), "bytes over HTTP")
```

```bash
go run .
```

```
fetched 559 bytes over HTTP
```

(The byte count depends on what the server returns today.)

The blank import is doing the work: `httpfs`'s `init` registers file systems
for `http://` and `https://`. Nothing else in your code changed.

This is also the moment to learn the library's most important safety rule.
Comment the import out and run it again — you do not get a mysterious local
file named `https:/example.com`. You get an error, because **a string with a
`://` scheme that matches no registered file system resolves to the invalid
file system, never to a local path.**

## Step 4: List a directory

Create a few files and list them:

```go
	dir := fs.File("data")
	err = dir.MakeAllDirs()
	if err != nil {
		log.Fatal(err)
	}

	for _, name := range []string{"a.txt", "b.txt", "notes.md"} {
		err = dir.Join(name).WriteAllString(ctx, name)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Iterator form (Go 1.23+)
	for file, err := range dir.ListDirIter(ctx, "*.txt") {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("found:", file.Name(), file.Size())
	}
```

```
found: b.txt 5
found: a.txt 5
```

`notes.md` was filtered out by the `*.txt` pattern. Patterns match the file
**name**, not the whole path.

**Listing order is not guaranteed.** It is whatever the backend returns, which
for the local file system is directory order, not alphabetical. Sort explicitly
when order matters:

```go
files, err := dir.ListDirMax(ctx, -1, "*.txt")
if err != nil {
	log.Fatal(err)
}
fs.SortByName(files)
```

`fs.SortByPath`, `fs.SortBySize`, `fs.SortByModified` and
`fs.SortByNameDirsFirst` are there too.

There are four listing styles, and they exist for different jobs:

```go
// Callback: stop early by returning an error
err = dir.ListDir(ctx, func(f fs.File) error {
	fmt.Println(f.Name())
	return nil
}, "*.txt")

// Slice with a hard cap; -1 means all
files, err := dir.ListDirMax(ctx, 100, "*.txt")

// Recursive, into every sub-directory
err = dir.ListDirRecursive(ctx, func(f fs.File) error {
	fmt.Println(f.Path())
	return nil
})

// Glob with wildcard segments
for file, segments := range fs.MustGlob(ctx, "data/*.txt") {
	fmt.Println(segments, file.Path())
}
```

Reach for `ListDirIter` by default, `ListDirMax` when you want a slice, and the
callback form when you need to stop on a condition.

## Step 5: Accept files you don't own with FileReader

A function that only reads should take `fs.FileReader`, not `fs.File`:

```go
func describe(ctx context.Context, f fs.FileReader) error {
	text, err := f.ReadAllString(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d bytes, starts with %q\n", f.Name(), f.Size(), firstLine(text))
	return nil
}

func firstLine(s string) string {
	if i := len(s); i > 20 {
		return s[:20] + "…"
	}
	return s
}
```

Now both a real file and an in-memory one satisfy it:

```go
	err = describe(ctx, fs.File("hello.txt"))
	if err != nil {
		log.Fatal(err)
	}
	err = describe(ctx, fs.NewMemFile("inline.txt", []byte("just some bytes")))
	if err != nil {
		log.Fatal(err)
	}
```

```
hello.txt: 13 bytes, starts with "Hello, go-fs!"
inline.txt: 15 bytes, starts with "just some bytes"
```

`fs.MemFile` is a filename plus a byte slice, nothing more:

```go
type MemFile struct {
	FileName string
	FileData []byte
}
```

It is not a `File`, because it has no backing file system — its name is just a
name. But it implements `FileReader`, so any function written against that
interface accepts it. That is the pattern to use for uploads, test fixtures and
generated content.

One caveat: `MemFile.ReadAll` returns `FileData` directly, without copying.
Do not modify the returned slice unless you mean to modify the file.

## Step 6: Read and write JSON

```go
type Config struct {
	Name  string `json:"name"`
	Debug bool   `json:"debug"`
}

	cfg := Config{Name: "tutorial", Debug: true}

	err = fs.File("config.json").WriteJSON(ctx, &cfg, "  ") // indented
	if err != nil {
		log.Fatal(err)
	}

	var loaded Config
	err = fs.File("config.json").ReadJSON(ctx, &loaded)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%+v\n", loaded)
```

```
{Name:tutorial Debug:true}
```

`ReadXML` and `WriteXML` are the same for XML. Because `File` is a string type,
it also marshals as a plain string inside your own structs with no extra code:

```go
type Job struct {
	Input  fs.File `json:"input"`  // "s3://bucket/in.csv"
	Output fs.File `json:"output"` // "/srv/out.csv"
}
```

That works in JSON, XML, YAML and SQL drivers, because reflection sees
`reflect.String`.

## Step 7: Clean up

```go
	err = fs.File("data").RemoveRecursive(ctx)
	if err != nil {
		log.Fatal(err)
	}
	err = fs.Remove("hello.txt", "config.json") // skips missing files
	if err != nil {
		log.Fatal(err)
	}
```

`fs.Remove` takes URIs and ignores files that are already gone, which is
usually what cleanup wants. `file.Remove()` reports a missing file as an error;
wrap it in `fs.RemoveErrDoesNotExist` if you would rather not care.

## What you built

A program that reads, writes, lists and cleans up files, running unchanged
against the local disk, an in-memory file system and an HTTP server.

What you actually learned:

- **`fs.File` is a string.** String literals work, `const` works, and it
  marshals for free. See [Why File is a string](explanation-the-file-type.md).
- **The URI prefix selects the backend.** Importing a backend registers it; an
  unmatched scheme is an error, never a silent local write. See
  [Paths, URIs and the registry](explanation-path-and-uri-model.md).
- **`ctx` appears only where an operation can take long.** See
  [the context rule](explanation-context-rule.md).
- **`fs.FileReader` is the read-only interface** that both `File` and `MemFile`
  implement.

## Next steps

- [Connect a remote backend](howto-connect-a-remote-backend.md) — S3, SFTP, SMB, WebDAV, Azure, Dropbox
- [Test with MemFileSystem](howto-test-with-memfilesystem.md) — make your own code testable
- [Serve files over HTTP](howto-serve-files-over-http.md)
- [Work with archives](howto-work-with-archives.md) — ZIP and tar as file systems
- [Copy, move and compare](howto-copy-move-and-compare.md)
- [Implement a file system](tutorial-implement-a-filesystem.md) — write your own backend
- The full API on [pkg.go.dev](https://pkg.go.dev/github.com/ungerik/go-fs)
