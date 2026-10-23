package webdavfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/webdavfs"
)

// ExampleNew connects to a WebDAV share and registers it,
// so that its files can also be used by URI.
//
// The example is compiled but not run, because it needs a WebDAV server.
func ExampleNew() {
	ctx := context.Background()

	webdavFS, err := webdavfs.New(
		ctx,
		"https://example.com/remote.php/dav/files/erik",
		&webdavfs.Options{Username: "erik", Password: "secret"},
	)
	if err != nil {
		panic(err)
	}
	if err := fs.Register(webdavFS); err != nil {
		panic(err)
	}
	defer webdavFS.Close() // also unregisters it

	// webdav://erik@example.com/remote.php/dav/files/erik/Documents/notes.txt
	file := webdavFS.RootDir().Join("Documents", "notes.txt")
	content, err := file.ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
