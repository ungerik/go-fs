package smbfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs/smbfs"
)

// ExampleDialAndRegister mounts an SMB share as file system.
//
// The example is compiled but not run, because it needs an SMB server.
func ExampleDialAndRegister() {
	ctx := context.Background()

	smbFS, err := smbfs.DialAndRegister(
		ctx,
		"smb://fileserver/share",
		&smbfs.Options{Username: "erik", Password: "secret"},
	)
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
