package sftpfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs/sftpfs"
)

// ExampleDialAndRegister connects to an SSH server and registers it as
// file system, so its files can be used through the File API.
//
// The example is compiled but not run, because it needs an SSH server.
func ExampleDialAndRegister() {
	ctx := context.Background()

	sftpFS, err := sftpfs.DialAndRegister(
		ctx,
		"sftp://example.com:22",
		sftpfs.UsernameAndPassword("erik", "secret"),
		sftpfs.AcceptAnyHostKey, // use ssh.FixedHostKey in production
		nil,                     // optional connection logger
	)
	if err != nil {
		panic(err)
	}
	defer sftpFS.Close()

	content, err := sftpFS.RootDir().Join("home", "erik", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
