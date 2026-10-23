package ftpfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/ftpfs"
)

// ExampleEnsureRegistered connects to an FTP server and registers it as
// file system, so its files can be used through the File API.
// The URIs of the file system contain the username but never the password.
//
// The example is compiled but not run, because it needs an FTP server.
func ExampleEnsureRegistered() {
	ctx := context.Background()

	free, err := ftpfs.EnsureRegistered(ctx, "ftp://erik@example.com", &ftpfs.Options{Password: "secret"})
	if err != nil {
		panic(err)
	}
	defer free()

	content, err := fs.File("ftp://erik@example.com/pub/notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}

// ExampleDial dials an FTPS server without registering the file system.
//
// The example is compiled but not run, because it needs an FTP server.
func ExampleDial() {
	ctx := context.Background()

	ftpFS, err := ftpfs.Dial(ctx, "ftps://example.com", &ftpfs.Options{
		Username: "erik",
		Password: "secret",
	})
	if err != nil {
		panic(err)
	}
	defer ftpFS.Close()

	content, err := ftpFS.RootDir().Join("pub", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
