# Dropbox File System

A [go-fs](https://github.com/ungerik/go-fs) file system for a Dropbox account.

## Installation

```bash
go get github.com/ungerik/go-fs/dropboxfs
```

## Usage

```go
import (
    "context"
    "time"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/dropboxfs"
)

func main() {
    ctx := context.Background()

    // Fetches the account id, doesn't register the file system
    dbxFS, err := dropboxfs.New(ctx, accessToken, &dropboxfs.Options{
        CacheTimeout: 5 * time.Minute, // metadata cache, zero disables it
        Mute:         true,            // no change notifications for the owner
    })
    if err != nil {
        panic(err)
    }
    // Register it so that fs.File values with its prefix work
    if err := fs.Register(dbxFS); err != nil {
        panic(err)
    }
    defer dbxFS.Close() // also unregisters it

    // The prefix is dropbox://<account id without "dbid:">, so it is
    // stable per account, and ID() is the account id with "dbid:"
    file := dbxFS.RootDir().Join("notes.md")

    err = file.WriteAllString(ctx, "Hello, Dropbox!")
    content, err := file.ReadAllString(ctx)
}
```

`NewAndRegister(ctx, accessToken, opts)` registers the file system and
returns only an error. It is meant for programs that know the account id and
address files by URI; `fs.GetFileSystem(uri)` returns the registered file
system. A nil `*Options` uses the defaults: no cache, notifications on.

`Options.Mute` keeps Dropbox from notifying the account owner of the changes
made through the file system. Set it for automated operations that should
not spam the account owner.

### Short-lived tokens

Dropbox issues short-lived access tokens (4 hours) for apps created since
2021. A long running program needs a refresh token and a client that
refreshes the access token. Pass such a client as `Options.HTTPClient`, the
access token argument is ignored then:

```go
import (
    "github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
    "golang.org/x/oauth2"
)

conf := &oauth2.Config{
    ClientID:     appKey,
    ClientSecret: appSecret,
    Endpoint:     dropbox.OAuthEndpoint(""),
}
client := conf.Client(ctx, &oauth2.Token{RefreshToken: refreshToken})
dbxFS, err := dropboxfs.New(ctx, "", &dropboxfs.Options{HTTPClient: client})
```

## How Dropbox concepts map to the file system

- **Case.** Dropbox paths are case-insensitive: `/Docs/a.txt` and
  `/docs/A.TXT` are the same file. FileInfos carry the names in the case
  Dropbox reports, usually the one they were created with. A `Move` that only
  changes the case of a name works, `CopyFile` onto the same path in another
  case is a no-op.
- **Names.** The file system implements `fs.ValidateNameFileSystem`: a copy
  or move into it fails before it writes anything at a name that is not
  valid UTF-8, whose bytes the JSON of the API would replace, or that
  contains a NUL character.
- **Folders.** Like the Dropbox API, `MakeDir`, uploads, `Move` and
  `CopyFile` create missing parent folders.
- **Uploads.** Files of up to 150 MiB are uploaded with one request, larger
  ones with an upload session in chunks of 32 MiB. Writers from
  `OpenWriter` (also used by copies from other file systems) upload a file
  that is larger than one chunk while it is written, so they hold at most
  one chunk in memory. The writers implement `fs.WriteAborter`: `Abort`
  discards the data, a started upload session is never committed and the
  file stays unchanged.
- **Move and copy.** Dropbox refuses to move or copy onto an existing path,
  so an existing destination file is deleted first, but only in the version
  that was checked: a folder or a newer file that took its place is never
  deleted. The replaced file stays restorable in the Dropbox version history.
  An existing destination folder is an error wrapping `os.ErrExist` for
  `Move` and an `fs.ErrIsDirectory` for `CopyFile`, a folder moved onto a
  file is an `fs.ErrIsNotDirectory`, and a folder moved into itself an error
  wrapping `os.ErrInvalid`. The source is checked on the server before the
  destination file is deleted, so a `CopyFile` of a cached file that became
  a folder returns an `fs.ErrIsDirectory` and keeps the destination.
- **Remove.** The Dropbox API deletes folders recursively, so `Remove` checks
  the type of the path on the server, never in the cache, and returns an
  `fs.ErrDirectoryNotEmpty` for a folder with content. A file is only deleted
  in the checked version. Content added to an empty folder between the check
  and the deletion is deleted with it.
- **Cache.** With `Options.CacheTimeout` the metadata of `Stat`, listings and
  writes is cached under case-insensitive keys. The cache holds at most
  10000 entries and evicts expired ones; every write invalidates the affected
  entries, including those below a moved or deleted folder.
- **Errors.** A missing path wraps `os.ErrNotExist`, reading or writing a
  folder is an `fs.ErrIsDirectory`, listing a file or writing below one an
  `fs.ErrIsNotDirectory`, `MakeDir` of an existing path wraps `os.ErrExist`.
  Transport, rate limit and authorization errors are returned unchanged and
  never mistaken for a missing file. After `Close` every method returns
  `fs.ErrFileSystemClosed`.
- **Permissions.** Dropbox has no permission model, the reported
  permissions are the constants `dropboxfs.DefaultPermissions` /
  `dropboxfs.DefaultDirPermissions`.

## Testing

The tests run offline without any configuration: `TestConformance` runs
the go-fs conformance suite against an in-memory fake of the Dropbox files
API (`fakeclient_test.go`) that models its case-insensitive paths, conflicts,
pagination and upload sessions. `TestConformanceOnline` runs the suite
against a real Dropbox account and skips unless `DROPBOX_ACCESS_TOKEN` is
set, so `go test ./...` stays green without credentials:

```bash
go test ./...
```

Running the live test is a manual step. **Never commit a token**: export it
in the shell that runs the tests, don't write it into a file inside the
repository.

### 1. Create a Dropbox app and an access token

1. Open the [Dropbox App Console](https://www.dropbox.com/developers/apps)
   and click *Create app*.
2. Choose *Scoped access* and the **App folder** access type, then name the
   app. App folder access confines everything the tests do to
   `Apps/<app name>/` and keeps the rest of the Dropbox out of reach — see
   the warning below for why that matters.
3. On the app's *Permissions* tab enable these scopes and click *Submit*:

   | Scope                  | Used for                               |
   | ---------------------- | -------------------------------------- |
   | `account_info.read`    | the account id that becomes the prefix |
   | `files.metadata.read`  | `Stat`, `ListDir`                      |
   | `files.metadata.write` | `MakeDir`, `Move`, `Remove`            |
   | `files.content.read`   | `OpenReader`, `ReadAll`                |
   | `files.content.write`  | `OpenWriter`, `WriteAll`, `CopyFile`   |

4. Back on the *Settings* tab, click *Generate* under *Generated access
   token* and copy the token. Generate it **after** submitting the scopes,
   a token issued earlier does not carry them.

The generated token is short-lived (4 hours), generate a new one for each
test session.

### 2. Set the environment variables

| Variable               | Required | Default        | Meaning                                                |
| ---------------------- | -------- | -------------- | ------------------------------------------------------ |
| `DROPBOX_ACCESS_TOKEN` | yes      | —              | the token from step 1, the live test skips without it  |
| `DROPBOX_TEST_DIR`     | no       | `/go-fs-test`  | directory the conformance suite works in               |
| `DROPBOX_MUTE`         | no       | unset          | `true` or `1` mutes the Dropbox change notifications   |

> **Warning:** the conformance suite creates, overwrites and deletes files
> below `DROPBOX_TEST_DIR` and removes everything inside that directory when
> it is done. Point it at a directory that exists only for testing, never at
> one holding real data. With an App folder app the path is relative to the
> app folder, so the default `/go-fs-test` becomes
> `Apps/<app name>/go-fs-test` and nothing outside it can be touched.

### 3. Run the tests

```bash
cd dropboxfs
 export DROPBOX_ACCESS_TOKEN='sl.your-token-here'
go test -v ./...
```

The space in front of `export` is intentional: it keeps the token out of the
shell history, provided the shell is configured for it
(`HISTCONTROL=ignorespace` in bash, `setopt HIST_IGNORE_SPACE` in zsh;
neither is on by default).

Only the live conformance suite:

```bash
go test -v -run 'TestConformanceOnline$' ./...
```

If the tests fail with `expired_access_token`, generate a new token. If they
fail with `missing_scope`, the app is missing one of the scopes from the
table above — add it and generate a new token afterwards.

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
