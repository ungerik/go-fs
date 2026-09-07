package tarfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/tarfs"
)

// ExampleNewWriter creates a gzip compressed tar archive in memory
// and reads it back with NewReader.
func ExampleNewWriter() {
	ctx := context.Background()

	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close()
	// The .tar.gz extension turns on gzip compression
	archive := memFS.RootDir().Join("docs.tar.gz")

	writer, err := tarfs.NewWriter(archive)
	if err != nil {
		panic(err)
	}
	err = writer.RootDir().Join("readme.txt").WriteAllString(ctx, "Hello tar")
	if err != nil {
		panic(err)
	}
	// Close finishes the archive and unregisters the file system
	err = writer.Close()
	if err != nil {
		panic(err)
	}

	reader, err := tarfs.NewReader(archive)
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
	// Hello tar
}
