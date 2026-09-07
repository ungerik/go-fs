package azureblobfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs/azureblobfs"
)

// ExampleNewFromConnectionString registers an Azure Blob Storage
// container as file system.
//
// The example is compiled but not run, because it needs a storage account.
func ExampleNewFromConnectionString() {
	ctx := context.Background()

	blobFS, err := azureblobfs.NewFromConnectionString(
		ctx,
		"DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...",
		"my-container",
		false, // read-only
	)
	if err != nil {
		panic(err)
	}
	defer blobFS.Close()

	content, err := blobFS.RootDir().Join("reports", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}
