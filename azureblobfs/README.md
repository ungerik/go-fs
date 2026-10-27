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
        nil,      // or &azureblobfs.Options{ReadOnly: true}
    )
    if err != nil {
        panic(err)
    }
    // Files resolve to their file system with the registry
    err = fs.Register(blobFS)
    if err != nil {
        panic(err)
    }
    defer blobFS.Close() // Also unregisters it

    // Use it like any other go-fs file system
    file := blobFS.RootDir().Join("images", "logo.png")

    err = file.WriteAll(ctx, logo)
    data, err := file.ReadAll(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.png")
}
```

- `New(ctx, client, opts)` takes a configured `*container.Client`, which is
  the way to use managed identities, SAS tokens or any other credential type
  the Azure SDK supports. It doesn't register the file system.
- `NewFromConnectionString(ctx, connectionString, container, opts)` builds
  the container client from a connection string, like `New` it doesn't
  register the file system.
- `NewAndRegister(ctx, client, opts)` creates and registers it in one step
  and returns only an error, wrapping `fs.ErrAlreadyRegistered` if another
  file system is registered for the container.

```go
client, err := container.NewClient(
    "https://myaccount.blob.core.windows.net/assets",
    credential, // e.g. from azidentity.NewDefaultAzureCredential
    nil,
)
err = azureblobfs.NewAndRegister(ctx, client, nil)
```

### Options

```go
&azureblobfs.Options{
    ReadOnly:           true, // writes return fs.ErrReadOnlyFileSystem
    SkipContainerCheck: true, // New sends no request
    DeleteSnapshots:    true, // Remove, RemoveAll and Move delete blob snapshots too
}
```

Unless `SkipContainerCheck` is set, `New` lists one blob to verify that the
container exists and can be listed. That works with account keys, Entra ID
roles and SAS tokens with the list permission, also container SAS tokens,
which don't allow reading the container properties that v0 requested.
Set it for credentials without the list permission, like an upload-only SAS;
writes then skip their directory check, which needs a listing too.

Azure refuses to delete a blob that has snapshots unless they are deleted
with it. By default the file system keeps such a blob: `Remove` returns an
error wrapping `azureblobfs.ErrSnapshotsPresent`, and `RemoveAll` first lists
the tree with its snapshots and returns that error before it deletes
anything (a snapshot that another client creates after that listing still
stops `RemoveAll` with the same error, after it deleted some blobs). `Move`
and `Rename` within the container copy the blobs server-side, without their
snapshots, and delete the source: they check for snapshots before they copy
anything, fail the same way and leave the source and the destination
untouched. A move to another file system copies first and fails when it
deletes the source. Set `DeleteSnapshots` to delete the snapshots together
with the blobs; deleted snapshots are gone unless soft delete is enabled for
the storage account.

File URIs have the form `azblob://<host>/<container>/<blob name>`, for
example `azblob://myaccount.blob.core.windows.net/assets/images/logo.png`.
The query of the container URL, like a SAS token, is not part of the URIs,
and the file system removes URL queries and SAS signatures from the
messages of the errors it returns, so they don't leak into logs.
`Name()` is `"Azure Blob Storage"`, `ID()` the prefix without the scheme.

## How Azure Blob concepts map to the file system

- **Paths and blob names.** File system paths are rooted
  (`/images/logo.png`), blob names are not (`images/logo.png`). Blobs that
  no path maps to, because their name starts with a slash or contains an
  empty element (`/x.txt`, `images//x.png`), are skipped by the listings.
  The file system implements `fs.ValidateNameFileSystem`, so a copy or move
  into it fails before it writes anything at a name that is not valid UTF-8,
  contains a backslash, which Blob Storage takes for a separator, or
  a control character, which the XML of a listing can't hold, ends with a
  dot, which Blob Storage removes, or is longer than 1024 characters. The
  service checks the 1024 characters of a whole blob name only when it is
  written.
- **Directories.** Blob Storage has no directories. A blob name prefix with
  at least one blob below it is an implicit directory. `MakeDir` creates a
  zero-byte marker blob with a trailing slash so empty directories exist
  too; `Stat`, `ListDir` and `Remove` treat both the same way. `MakeDir` on
  an existing path wraps `os.ErrExist`, `ListDir` of a missing directory
  wraps `os.ErrNotExist`, `Remove` of a directory with content returns
  `fs.ErrDirectoryNotEmpty`, and `RemoveAll` deletes everything below a
  prefix. `Remove` and `RemoveAll` refuse the root directory with an error
  wrapping `os.ErrInvalid`; `RemoveAll` of it would delete every blob of
  the container. Writing or copying onto a directory returns `fs.ErrIsDirectory`
  instead of creating a blob that would turn the directory into a file.
- **Native operations.** `ReadAll`, `WriteAll`, `OpenReadWriter`, `Touch`,
  `RemoveAll`, `ListDirRecursive`, `CopyFile` and `Move` are native, and readers
  seek with range requests that are pinned to the ETag of the blob when the
  reader was opened (`If-Match`): after the blob was replaced, the next range
  request fails with an error telling that the blob changed while it was
  being read, instead of returning bytes of the new blob after those of the
  old one. Everything else uses the generic emulation of
  the `fs` package. `ReadAll` allocates
  its buffer once for the `Content-Length` of the download, up to 16 MiB,
  so a huge blob grows the buffer while it is read instead of allocating
  all of it before the first byte arrived.
- **Streaming writes.** `OpenWriter` streams the written data into an
  upload: blocks of 4 MiB are staged while the data is written and committed
  on `Close`, smaller data is uploaded with one request on `Close`. Memory
  stays bounded and the blob only appears or changes when `Close` completes.
  The writers implement `fs.WriteAborter`: `Abort` cancels the upload,
  nothing is committed (Azure deletes uncommitted blocks after a week).
- **Copy, move and touch.** `CopyFile` is a server-side copy. `Move` copies
  the blobs server-side and deletes the source after every copy succeeded,
  Azure has no rename. Every copy and delete of `Move` is pinned to the ETag
  that the source blob had when `Move` found it, so a blob that another
  client replaces during the move is never deleted: `Move` keeps it and
  returns an error telling that the blob changed while it was being moved
  (after its copy, the destination holds the copy of the old version).
  The copies are pinned to the destination as well: to no blob
  (`If-None-Match: *`) for a destination that `Move` found missing, like
  every copy of a directory, and to the ETag of an existing destination blob
  (`If-Match`). So `Move` never overwrites a blob that another client creates
  at the destination, or below the destination of a directory, after `Move`
  checked it, nor a newer version of the destination blob: the copy fails
  (also for a deleted destination blob), and `Move` stops with an error
  wrapping `os.ErrExist` (`fs.ErrAlreadyExists` for the destination blob)
  and deletes no source blob. A source blob that another client deletes
  before its copy stops `Move` the same way with an error wrapping
  `os.ErrNotExist`; the copies made before stay at the destination. One
  deleted after its copy is no error, also not when the SDK retried a delete
  whose answer was lost: `Move` looks a source blob up after a 412 answer to
  its pinned delete, which Azurite also sends for a missing blob, and goes on
  if it is gone.
  When the SDK retries a copy whose answer was lost (like after a 500 answer
  or a broken connection) and the retry fails the destination condition against
  the copy that Azure already made, `Move` takes a destination blob that is
  the completed copy of the source blob it found (copied from the source blob
  itself, not from a snapshot or version of it, with the same size and
  `Content-MD5`, completed after the source was last modified) as its copy
  and goes on.
  `Touch` updates the modification time by setting the metadata of the blob
  again, which keeps the metadata (with the case of their names), the
  content headers and the content. `Touch` of a missing path creates an
  empty blob with a conditional request, so a blob that another client
  created in the meantime is not overwritten.
- **Permissions.** Access is governed by Azure roles and SAS tokens, the
  reported permissions are the constants `azureblobfs.DefaultPermissions` /
  `azureblobfs.DefaultDirPermissions`. There are no symbolic links and no
  append; `Append` is emulated by rewriting the blob.
- **Errors.** Missing blobs wrap `os.ErrNotExist`; removing a blob with
  snapshots without `Options.DeleteSnapshots` wraps
  `azureblobfs.ErrSnapshotsPresent`; after `Close` every
  method returns `fs.ErrFileSystemClosed`, also the `Close` of a writer that
  was opened before.

## Testing

The tests run the go-fs conformance suite against
[Azurite](https://github.com/Azure/Azurite), the Azure Storage emulator, in
a Docker container (`mcr.microsoft.com/azure-storage/azurite`), plus tests
for container SAS tokens, streaming writers and the regressions of the v1
review. Azurite stores metadata names in lower case, so the case
preservation of `Touch` is only exercised on Azure. Docker is required for
most tests. They are skipped when Docker is unavailable, meaning the
`docker` command is not installed or its daemon doesn't answer
`docker info`. When Docker is available but the container can't be started,
the test run fails instead of skipping, except on Windows, where Docker
usually can't run the Linux image and the tests are skipped. The offline
tests (constructors, error redaction, writers) always run.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
