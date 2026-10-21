package azureblobfs_test

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/azureblobfs"
)

// ExampleNewFromConnectionString creates a file system for an Azure Blob
// Storage container and registers it. Files resolve to their file system
// with the registry, so the file system must be registered to use it via
// File values.
//
// The example is compiled but not run, because it needs a storage account.
func ExampleNewFromConnectionString() {
	ctx := context.Background()

	blobFS, err := azureblobfs.NewFromConnectionString(
		ctx,
		"DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...",
		"my-container",
		nil, // or &azureblobfs.Options{ReadOnly: true}
	)
	if err != nil {
		panic(err)
	}
	err = fs.Register(blobFS)
	if err != nil {
		panic(err)
	}
	defer blobFS.Close() // Also unregisters it

	content, err := blobFS.RootDir().Join("reports", "notes.txt").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(content)
}

// ExampleNewAndRegister registers a container that is accessed with a
// container SAS URL. A SAS without the list permission, like one that only
// allows uploads, needs Options.SkipContainerCheck.
//
// The example is compiled but not run, because it needs a storage account.
func ExampleNewAndRegister() {
	ctx := context.Background()

	client, err := container.NewClientWithNoCredential(
		"https://myaccount.blob.core.windows.net/uploads?sv=...&sp=cw&sig=...",
		nil,
	)
	if err != nil {
		panic(err)
	}
	err = azureblobfs.NewAndRegister(ctx, client, &azureblobfs.Options{SkipContainerCheck: true})
	if err != nil {
		panic(err)
	}
	file := fs.File("azblob://myaccount.blob.core.windows.net/uploads/report.csv")
	defer file.FileSystem().Close() // Unregisters it

	err = file.WriteAllString(ctx, "id,value\n1,42\n")
	if err != nil {
		panic(err)
	}
}
