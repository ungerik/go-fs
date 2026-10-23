package dropboxfs_test

import (
	"context"
	"fmt"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/dropboxfs"
)

// ExampleNew connects to a Dropbox account and registers it,
// so that its files can also be used by URI.
//
// The example is compiled but not run, because it needs an access token.
func ExampleNew() {
	ctx := context.Background()

	dropboxFS, err := dropboxfs.New(ctx, "my-access-token", &dropboxfs.Options{
		CacheTimeout: time.Minute, // metadata cache timeout, 0 disables the cache
		Mute:         true,        // don't notify the account owner of changes
	})
	if err != nil {
		panic(err)
	}
	if err := fs.Register(dropboxFS); err != nil {
		panic(err)
	}
	defer dropboxFS.Close() // also unregisters it

	// dropbox://<account id>/Documents/notes.txt
	content, err := dropboxFS.RootDir().Join("Documents", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
