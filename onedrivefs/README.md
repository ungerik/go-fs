# OneDrive and SharePoint File System

A [go-fs](https://github.com/ungerik/go-fs) file system for a drive of
Microsoft OneDrive or SharePoint over the Microsoft Graph API: the OneDrive
of a user (OneDrive Personal or OneDrive for Business) or a document library
of a SharePoint site.

## Installation

Get the backend together with the go-fs core module, at the same version:

```bash
go get github.com/ungerik/go-fs@v1.0.0-rc.6 github.com/ungerik/go-fs/onedrivefs@v1.0.0-rc.6
```

## Usage

```go
import (
    "context"
    "os"

    "golang.org/x/oauth2/clientcredentials"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/onedrivefs"
)

func main() {
    ctx := context.Background()

    // An app of Microsoft Entra ID with the application permission
    // Sites.Selected, Sites.ReadWrite.All or Files.ReadWrite.All
    secret := os.Getenv("ONEDRIVE_CLIENT_SECRET")
    credentials := &clientcredentials.Config{
        ClientID:     clientID,
        ClientSecret: secret,
        TokenURL:     "https://login.microsoftonline.com/" + tenantID + "/oauth2/v2.0/token",
        Scopes:       []string{"https://graph.microsoft.com/.default"},
    }

    // Fetches the drive id, doesn't register the file system
    driveFS, err := onedrivefs.New(ctx, "sites/"+siteID+"/drive", credentials.TokenSource(ctx), nil)
    if err != nil {
        panic(err)
    }
    // Register it so that fs.File values with its prefix work
    if err := fs.Register(driveFS); err != nil {
        panic(err)
    }
    defer driveFS.Close() // also unregisters it

    // The prefix is onedrive://<drive id>, so it is stable per drive
    file := driveFS.RootDir().Join("Reports", "2026.md")

    err = file.WriteAllString(ctx, "Hello, SharePoint!")
    content, err := file.ReadAllString(ctx)
}
```

The drive argument of `New` is a drive id or the Graph API path of a drive:

| Drive                                   | Argument                                   | Access token |
| --------------------------------------- | ------------------------------------------ | ------------ |
| OneDrive of the signed-in user          | `me/drive`                                 | delegated    |
| OneDrive of a user                      | `users/<user id or principal name>/drive`  | any          |
| Default document library of a site      | `sites/<site id>/drive`                    | any          |
| Document library of a Microsoft 365 group | `groups/<group id>/drive`                | any          |
| Any drive by id                         | `<drive id>` or `drives/<drive id>`        | any          |

`NewAndRegister(ctx, drive, tokens, opts)` registers the file system and
returns only an error. It is meant for programs that know the drive id and
address files by URI; `fs.GetFileSystem(uri)` returns the registered file
system. A nil `*Options` uses the defaults: `http.DefaultClient` and the
Graph API of the global Microsoft cloud.

### Access tokens

`New` takes an `oauth2.TokenSource` and adds its tokens only to the requests
of the Graph API. The URLs that Graph returns for downloads, upload sessions
and copy monitors are pre-authenticated and on other hosts, so they must not
get the token, and upload sessions reject it. Pass a plain client as
`Options.HTTPClient`, not one that authenticates its requests.

- **Server apps** use the client credentials of an app of Microsoft Entra ID,
  as above. With the application permission `Sites.Selected` the app reaches
  only the SharePoint sites an admin granted it.
- **Apps acting for a user** get delegated tokens with the permission
  `Files.ReadWrite` (or `Files.ReadWrite.All` for files shared with the
  user). Pass the `TokenSource` of an `oauth2.Config` with the refresh token
  of the user, which refreshes the access tokens:

  ```go
  conf := &oauth2.Config{
      ClientID: clientID,
      Endpoint: oauth2.Endpoint{
          AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
          TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
      },
      Scopes: []string{"Files.ReadWrite", "offline_access"},
  }
  driveFS, err := onedrivefs.New(ctx, "me/drive", conf.TokenSource(ctx, token), nil)
  ```

- A program that gets its tokens from `azidentity` wraps its credential in
  a `TokenSource` that calls `GetToken` with the scope
  `https://graph.microsoft.com/.default`.

`Options.GraphURL` selects a national cloud, like
`https://graph.microsoft.us/v1.0` for US Government L4.

## How OneDrive concepts map to the file system

- **Case.** OneDrive and SharePoint paths are case-insensitive: `/Docs/a.txt`
  and `/docs/A.TXT` are the same file. FileInfos carry the names in the case
  the server reports, usually the one they were created with. A `Move` that
  only changes the case of a name works, `CopyFile` onto the same path in
  another case is a no-op.
- **Names.** The file system implements `fs.ValidateNameFileSystem`: a copy
  or move into it fails before it writes anything at a name that is not
  valid UTF-8, contains a control character or one of `" * : < > ? \ |`,
  begins or ends with a space, ends with a dot, or is reserved: `.lock`,
  `desktop.ini`, names beginning with `~$` or containing `_vti_`, and the
  Windows device names like `CON` or `nul.txt`. `forms` is reserved only in
  the root folder of a SharePoint library and is not checked.
- **Folders.** Uploads create missing parent folders, `MakeDir` doesn't.
  A OneNote notebook is listed as a folder. A shortcut to a folder shared
  from another drive is listed as neither file nor folder.
- **Uploads.** Files of up to 4 MiB are uploaded with one request, larger
  ones with an upload session in fragments of 10 MiB. An upload session needs
  the size of the file before the first fragment, so a writer from
  `OpenWriter` (also used by copies from other file systems) keeps up to
  4 MiB in memory and writes a larger file to a temporary file of
  `os.TempDir()`, which `Close` uploads and removes. `WriteAll` knows the
  size and uploads directly. The writers implement `fs.WriteAborter`:
  `Abort` discards the data and the temporary file, a failed upload cancels
  its session, and the file stays unchanged.
- **Move and copy.** Both run on the server; `CopyFile` waits for the copy
  to complete. Graph refuses to move or copy onto an existing name, so an
  existing destination file is deleted first, but only in the version (eTag)
  that was checked: a folder or a newer file that took its place is never
  deleted. The replaced file stays restorable in the recycle bin.
  An existing destination folder is an error wrapping `os.ErrExist` for
  `Move` and an `fs.ErrIsDirectory` for `CopyFile`, a folder moved onto a
  file is an `fs.ErrIsNotDirectory`, a folder moved into itself an error
  wrapping `os.ErrInvalid`, and a missing destination folder an
  `fs.ErrDoesNotExist`.
- **Remove.** Graph deletes folders recursively, so `Remove` checks the type
  and the child count of the item first and returns an
  `fs.ErrDirectoryNotEmpty` for a folder with content. Only the checked
  version (eTag) is deleted. `RemoveAll` deletes a folder with one request.
  Deleted items go to the recycle bin of the drive.
- **Touch.** `Touch` creates an empty file, or sets the modification time
  of the `fileSystemInfo` of an existing item, which `FileInfo.Modified`
  reports.
- **Throttling.** Requests answered with 429 or 503 are repeated up to four
  times after the delay of their `Retry-After` header or an exponential
  backoff.
- **Errors.** A missing path wraps `os.ErrNotExist`, reading or writing a
  folder is an `fs.ErrIsDirectory`, listing a file or writing below one an
  `fs.ErrIsNotDirectory`, `MakeDir` of an existing path wraps `os.ErrExist`,
  a 403 wraps `os.ErrPermission` together with the reason Graph gives.
  Other error responses are an `*onedrivefs.APIError` with the status code
  and the Graph error code, like an expired token (401) or throttling that
  outlasted the retries (429); they are never mistaken for a missing file.
  The errors never contain the access token or the pre-authenticated URLs.
  After `Close` every method returns `fs.ErrFileSystemClosed`.
- **Permissions.** The reported permissions are the constants
  `onedrivefs.DefaultPermissions` / `onedrivefs.DefaultDirPermissions`.
- **No metadata cache.** Every `Stat` and listing asks the server.

## Testing

The tests run offline without any configuration: `TestConformance` runs
the go-fs conformance suite against an in-memory fake of the drive API of
Graph (`fakegraph_test.go`) that models its case-insensitive paths, conflicts,
pagination, upload sessions, eTags, asynchronous copies, and the
pre-authenticated URLs that reject the access token. `TestConformanceOnline`
runs the suite against a real drive and skips unless `ONEDRIVE_ACCESS_TOKEN`
is set, so `go test ./...` stays green without credentials:

```bash
go test ./...
```

Running the live test is a manual step. **Never commit a token**: export it
in the shell that runs the tests, don't write it into a file inside the
repository.

### 1. Get an access token

The quickest way is [Graph Explorer](https://developer.microsoft.com/graph/graph-explorer):
sign in with a test account, consent to `Files.ReadWrite`, and copy the
access token from the *Access token* tab. It is valid for about an hour.

### 2. Set the environment variables

| Variable                | Required | Default       | Meaning                                                  |
| ----------------------- | -------- | ------------- | -------------------------------------------------------- |
| `ONEDRIVE_ACCESS_TOKEN` | yes      | —             | the token from step 1, the live test skips without it    |
| `ONEDRIVE_DRIVE`        | no       | `me/drive`    | the drive argument of `New`                              |
| `ONEDRIVE_TEST_DIR`     | no       | `/go-fs-test` | directory the conformance suite works in, made if needed |

> **Warning:** the conformance suite creates, overwrites and deletes files
> below `ONEDRIVE_TEST_DIR` and removes everything inside that directory when
> it is done. Point it at a directory that exists only for testing, in a
> drive without real data.

### 3. Run the tests

```bash
cd onedrivefs
 export ONEDRIVE_ACCESS_TOKEN='eyJ0eXAi...'
go test -v -run 'TestConformanceOnline$' ./...
```

The space in front of `export` keeps the token out of the shell history,
provided the shell is configured for it (`HISTCONTROL=ignorespace` in bash,
`setopt HIST_IGNORE_SPACE` in zsh).

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
