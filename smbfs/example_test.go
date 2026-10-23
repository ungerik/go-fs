package smbfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/smbfs"
)

// ExampleEnsureRegistered mounts an SMB share as registered file system,
// so its files can be used through the File API.
// The URIs contain the username but never the password.
//
// The example is compiled but not run, because it needs an SMB server.
func ExampleEnsureRegistered() {
	ctx := context.Background()

	free, err := smbfs.EnsureRegistered(ctx, "smb://erik@fileserver/share", &smbfs.Options{Password: "secret"})
	if err != nil {
		panic(err)
	}
	defer free()

	content, err := fs.File("smb://erik@fileserver/share/Documents/notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}

// ExampleDial mounts an SMB share without registering the file system.
//
// The example is compiled but not run, because it needs an SMB server.
func ExampleDial() {
	ctx := context.Background()

	smbFS, err := smbfs.Dial(ctx, "smb://fileserver/share", &smbfs.Options{
		Username: "erik",
		Password: "secret",
		Domain:   "CORP",
	})
	if err != nil {
		panic(err)
	}
	defer smbFS.Close()

	content, err := smbFS.RootDir().Join("Documents", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
