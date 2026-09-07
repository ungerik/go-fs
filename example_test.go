package fs_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing/fstest"

	"github.com/ungerik/go-fs"
)

// ExampleFile shows the path methods of File, which are pure string
// operations that don't touch a file system.
//
// Path returns the file system specific path, which uses the separator of
// the operating system for local files, PathWithSlashes always uses "/".
func ExampleFile() {
	file := fs.File("/home/erik/data/report.pdf")

	fmt.Println(file.Name())
	fmt.Println(file.Ext())
	fmt.Println(file.Dir().PathWithSlashes())
	fmt.Println(file.Dir().Join("other.txt").PathWithSlashes())

	// Output:
	// report.pdf
	// .pdf
	// /home/erik/data
	// /home/erik/data/other.txt
}

// ExampleFile_uri shows that a File is either a local path or a URI with
// the prefix of a registered file system. Which one it is follows from the
// prefix, and the file system of an unregistered prefix is invalid.
func ExampleFile_uri() {
	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close()

	local := fs.File("/var/log/messages")
	inMemory := memFS.RootDir().Join("data", "report.pdf")
	unregistered := fs.File("s3://some-bucket/report.pdf")

	fmt.Println(local.FileSystem().Name())
	fmt.Println(inMemory.FileSystem().Name())
	fmt.Println(unregistered.FileSystem().Name())

	// LocalPath is empty for a file that is not on the local file system
	fmt.Println(inMemory.Name(), inMemory.LocalPath() == "")

	// Output:
	// local file system
	// memory file system
	// invalid file system
	// report.pdf true
}

// ExampleNewMemFileSystem writes and reads a file in memory,
// which is how tests avoid touching the disk.
func ExampleNewMemFileSystem() {
	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close() // unregisters the file system

	ctx := context.Background()
	file := memFS.RootDir().Join("greeting.txt")

	err = file.WriteAllString(ctx, "Hello World!")
	if err != nil {
		panic(err)
	}

	content, err := file.ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
	fmt.Println(file.Size())

	// Output:
	// Hello World!
	// 12
}

// ExampleFile_ListDir lists a directory. MemFileSystem does not
// guarantee an order, so the names are sorted before printing.
func ExampleFile_ListDir() {
	memFS, err := fs.NewMemFileSystem("/",
		fs.NewMemFile("a.txt", []byte("a")),
		fs.NewMemFile("b.txt", []byte("b")),
		fs.NewMemFile("c.json", []byte("{}")),
	)
	if err != nil {
		panic(err)
	}
	defer memFS.Close()

	var names []string
	err = memFS.RootDir().ListDir(
		context.Background(),
		func(file fs.File) error {
			names = append(names, file.Name())
			return nil
		},
		"*.txt", // optional patterns
	)
	if err != nil {
		panic(err)
	}
	slices.Sort(names)
	fmt.Println(names)

	// Output:
	// [a.txt b.txt]
}

// ExampleCopyFile copies a file between two different file systems.
func ExampleCopyFile() {
	ctx := context.Background()

	sourceFS, err := fs.NewMemFileSystem("/", fs.NewMemFile("data.txt", []byte("payload")))
	if err != nil {
		panic(err)
	}
	defer sourceFS.Close()

	destFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer destFS.Close()

	source := sourceFS.RootDir().Join("data.txt")
	dest := destFS.RootDir().Join("copy.txt")

	err = fs.CopyFile(ctx, source, dest)
	if err != nil {
		panic(err)
	}

	content, err := dest.ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)

	// Output:
	// payload
}

// ExampleNewStdFileSystem adapts an io/fs.FS of the standard library,
// here a testing/fstest.MapFS, so it can be used with the File API.
// An embed.FS or os.DirFS works the same way.
func ExampleNewStdFileSystem() {
	stdFS := fs.NewStdFileSystem(
		fstest.MapFS{
			"config/app.json": &fstest.MapFile{Data: []byte(`{"debug":true}`)},
		},
		"example", // id, part of the URI prefix
	)
	fs.Register(stdFS)
	defer fs.Unregister(stdFS)

	file := stdFS.RootDir().Join("config", "app.json")
	content, err := file.ReadAllString(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(file)
	fmt.Println(content)

	// Output:
	// stdfs://example/config/app.json
	// {"debug":true}
}

// ExampleNewSubFileSystem exposes a directory of another file system
// as a file system of its own that paths can't escape.
func ExampleNewSubFileSystem() {
	ctx := context.Background()

	memFS, err := fs.NewMemFileSystem("/", fs.NewMemFile("uploads/photo.jpg", []byte("JPEG")))
	if err != nil {
		panic(err)
	}
	defer memFS.Close()

	subFS, err := fs.NewSubFileSystem(memFS, "/uploads", "uploads")
	if err != nil {
		panic(err)
	}
	fs.Register(subFS)
	defer fs.Unregister(subFS)

	file := subFS.RootDir().Join("photo.jpg")
	content, err := file.ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(file)
	fmt.Println(content)

	// Output:
	// sub://uploads/photo.jpg
	// JPEG
}

// ExampleNewOverlayFileSystem makes a read-only file system writable
// by stacking a writable layer on top of it.
func ExampleNewOverlayFileSystem() {
	ctx := context.Background()

	base := fs.NewStdFileSystem(
		fstest.MapFS{"defaults.conf": &fstest.MapFile{Data: []byte("timeout=30")}},
		"overlay-base",
	)
	fs.Register(base)
	defer fs.Unregister(base)

	upper, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer upper.Close()

	overlay, err := fs.NewOverlayFileSystem(base, upper, "example")
	if err != nil {
		panic(err)
	}
	fs.Register(overlay)
	defer fs.Unregister(overlay)

	// Writing a base file copies it up into the writable upper layer
	err = overlay.RootDir().Join("defaults.conf").WriteAllString(ctx, "timeout=60")
	if err != nil {
		panic(err)
	}

	content, err := overlay.RootDir().Join("defaults.conf").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)

	// The base is untouched
	baseContent, err := base.RootDir().Join("defaults.conf").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(baseContent)

	// Output:
	// timeout=60
	// timeout=30
}

// ExampleErrDoesNotExist shows the error contract: every file system
// reports a missing file as an error wrapping os.ErrNotExist.
func ExampleErrDoesNotExist() {
	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close()

	_, err = memFS.RootDir().Join("missing.txt").ReadAll(context.Background())

	fmt.Println(errors.Is(err, os.ErrNotExist))

	if notExist, ok := errors.AsType[fs.ErrDoesNotExist](err); ok {
		file, _ := notExist.File()
		fmt.Println(file.Name())
	}

	// Output:
	// true
	// missing.txt
}

// ExampleMemFile shows MemFile, a FileReader of a name and a byte slice
// that needs no file system at all.
func ExampleMemFile() {
	memFile := fs.NewMemFile("notes.txt", []byte("remember"))

	fmt.Println(memFile.Name())
	fmt.Println(memFile.Size())

	content, err := memFile.ReadAllString(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(content)

	// Output:
	// notes.txt
	// 8
	// remember
}
