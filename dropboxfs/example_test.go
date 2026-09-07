package dropboxfs_test

import (
	"context"
	"fmt"
	"time"

	"github.com/ungerik/go-fs/dropboxfs"
)

// ExampleNewAndRegister registers a Dropbox account as file system.
//
// The example is compiled but not run, because it needs an access token.
func ExampleNewAndRegister() {
	ctx := context.Background()

	dropboxFS, err := dropboxfs.NewAndRegister(
		ctx,
		"my-access-token",
		time.Minute, // metadata cache timeout, 0 disables the cache
		false,       // mute the Dropbox SDK log output
	)
	if err != nil {
		panic(err)
	}
	defer dropboxFS.Close()

	content, err := dropboxFS.RootDir().Join("Documents", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
