# Azure Blob Storage File System

A [go-fs](https://github.com/ungerik/go-fs) file system for Azure Blob
Storage containers, built on the
[Azure SDK for Go](https://github.com/Azure/azure-sdk-for-go)
(`sdk/storage/azblob`).

## Installation

```bash
go get github.com/ungerik/go-fs/azureblobfs
```

## Usage

```go
import (
    "context"
    "os"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/azureblobfs"
)

func main() {
    ctx := context.Background()

    blobFS, err := azureblobfs.NewFromConnectionString(
        ctx,
        os.Getenv("AZURE_STORAGE_CONNECTION_STRING"),
        "assets", // container name
        false,    // read-only
    )
    if err != nil {
        panic(err)
    }
    defer blobFS.Close()

    // Use it like any other go-fs file system
    file := blobFS.RootDir().Join("images", "logo.png")

    err = file.WriteAll(ctx, logo)
    data, err := file.ReadAll(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.png")
}
```

`NewFromConnectionString` builds the container client from a connection
string and registers the file system. `NewAndRegister` takes a configured
`*container.Client`, which is the way to use managed identities, SAS tokens
or any other credential type the Azure SDK supports:

```go
client, err := container.NewClient(
    "https://myaccount.blob.core.windows.net/assets",
    credential, // e.g. from azidentity.NewDefaultAzureCredential
    nil,
)
blobFS, err := azureblobfs.NewAndRegister(ctx, client, false)
```

`New` does the same without registering the file system.

File URIs have the form `azblob://<host>/<container>/<blob name>`, for
example `azblob://myaccount.blob.core.windows.net/assets/images/logo.png`.
`blobFS.ID()` is that prefix without the scheme.

## How Azure Blob concepts map to the file system

- **Paths and blob names.** File system paths are rooted
  (`/images/logo.png`), blob names are not (`images/logo.png`).
- **Directories.** Blob Storage has no directories. A blob name prefix with
  at least one blob below it is an implicit directory. `MakeDir` creates a
  zero-byte marker blob with a trailing slash so empty directories exist
  too; `Stat`, `ListDir` and `Remove` treat both the same way. `MakeDir` on
  an existing path wraps `os.ErrExist`, `Remove` of a directory with content
  fails, and `RemoveAll` deletes everything below a prefix.
- **Native operations.** `ReadAll`, `WriteAll`, `OpenReadWriter`, `Touch`,
  `RemoveAll`, `ListDirRecursive` and `CopyFile` are native, and readers
  seek with range requests; everything else uses the generic emulation of
  the `fs` package.
- **Copy and touch.** `CopyFile` is a server-side copy. `Touch` updates the
  modification time by setting the blob metadata, because Blob Storage
  can't update it in place.
- **Permissions.** Access is governed by Azure roles and SAS tokens, the
  reported permissions are `azureblobfs.DefaultPermissions` /
  `azureblobfs.DefaultDirPermissions`. There are no symbolic links and no
  append; `Append` is emulated by rewriting the blob.
- **Errors.** Missing blobs wrap `os.ErrNotExist`; after `Close` every
  method returns `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against
[Azurite](https://github.com/Azure/Azurite), the Azure Storage emulator, in
a Docker container (`mcr.microsoft.com/azure-storage/azurite`). Docker is
required; if it is not installed the tests are skipped, if it is installed
but the container can't be started the tests fail.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
