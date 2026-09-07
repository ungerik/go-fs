package ftpfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs/ftpfs"
)

// ExampleDialAndRegister connects to an FTP server and registers it as
// file system, so its files can be used through the File API.
//
// The example is compiled but not run, because it needs an FTP server.
func ExampleDialAndRegister() {
	ctx := context.Background()

	ftpFS, err := ftpfs.DialAndRegister(
		ctx,
		"ftp://example.com:21",
		ftpfs.UsernameAndPassword("erik", "secret"),
		nil, // *ftpfs.Options, nil uses the defaults
	)
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
