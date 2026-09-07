package zipfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/zipfs"
)

// ExampleNewWriter creates a ZIP archive in memory
// and reads it back with NewReader.
func ExampleNewWriter() {
	ctx := context.Background()

	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close()
	archive := memFS.RootDir().Join("docs.zip")

	writer, err := zipfs.NewWriter(archive)
	if err != nil {
		panic(err)
	}
	err = writer.RootDir().Join("readme.txt").WriteAllString(ctx, "Hello ZIP")
	if err != nil {
		panic(err)
	}
	// Close finishes the archive and unregisters the file system
	err = writer.Close()
	if err != nil {
		panic(err)
	}

	reader, err := zipfs.NewReader(archive)
	if err != nil {
		panic(err)
	}
	defer reader.Close()

	content, err := reader.RootDir().Join("readme.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)

	// Output:
	// Hello ZIP
}
