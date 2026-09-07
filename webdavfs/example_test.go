package webdavfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs/webdavfs"
)

// ExampleNewAndRegister registers a WebDAV share as file system.
//
// The example is compiled but not run, because it needs a WebDAV server.
func ExampleNewAndRegister() {
	ctx := context.Background()

	webdavFS, err := webdavfs.NewAndRegister(
		ctx,
		"https://example.com/remote.php/dav/files/erik",
		&webdavfs.Options{Username: "erik", Password: "secret"},
	)
	if err != nil {
		panic(err)
	}
	defer webdavFS.Close()

	content, err := webdavFS.RootDir().Join("Documents", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
