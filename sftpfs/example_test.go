package sftpfs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/sftpfs"
)

// ExampleDial connects to an SSH server, verifying its host key with
// the known_hosts file of the user, and reads a file.
//
// The example is compiled but not run, because it needs an SSH server.
func ExampleDial() {
	ctx := context.Background()

	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	// knownhosts.New does not expand "~"
	hostKeyCallback, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		panic(err)
	}

	sftpFS, err := sftpfs.Dial(ctx, "sftp://erik@example.com", &sftpfs.Options{
		Password:        os.Getenv("SFTP_PASSWORD"),
		HostKeyCallback: hostKeyCallback,
	})
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

// ExampleEnsureRegistered shares one connection between the callers
// that use the same address and makes its files reachable via URIs.
//
// The example is compiled but not run, because it needs an SSH server.
func ExampleEnsureRegistered() {
	ctx := context.Background()

	free, err := sftpfs.EnsureRegistered(ctx, "sftp://erik@example.com:2222", &sftpfs.Options{
		Password:        os.Getenv("SFTP_PASSWORD"),
		HostKeyCallback: sftpfs.AcceptAnyHostKey, // use knownhosts.New in production
	})
	if err != nil {
		panic(err)
	}
	defer free() // closes the connection when the last caller frees it

	content, err := fs.File("sftp://erik@example.com:2222/home/erik/notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
