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

    // Fetches the account id, registers the file system
    dbxFS, err := dropboxfs.NewAndRegister(ctx, accessToken, 5*time.Minute, false)
    if err != nil {
        panic(err)
    }
    defer dbxFS.Close()

    // The prefix is dropbox://<account id>, so it is stable per account
    file := dbxFS.RootDir().Join("notes.md")

    err = file.WriteAllString(ctx, "Hello, Dropbox!")
    content, err := file.ReadAllString(ctx)
}
```

The third argument is the metadata cache timeout (zero disables the cache),
the fourth mutes the notifications Dropbox sends for changed files. Set it
for automated operations that should not spam the account owner.

## Testing

The unit tests in `errors_test.go` run offline without any configuration.
The two live tests in `dropboxfs_test.go` talk to a real Dropbox account and
skip unless `DROPBOX_ACCESS_TOKEN` is set, so `go test ./...` stays green
without credentials:

```bash
go test ./...
```

Running the live tests is a manual step. **Never commit a token**: export it
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

The generated token is short-lived (4 hours by default). Raise *Access token
expiration* to *No expiration* on the *Settings* tab for a token that lasts
between test runs, or simply generate a new one each time.

### 2. Set the environment variables

| Variable               | Required | Default        | Meaning                                                |
| ---------------------- | -------- | -------------- | ------------------------------------------------------ |
| `DROPBOX_ACCESS_TOKEN` | yes      | —              | the token from step 1, the live tests skip without it  |
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

Only the conformance suite, which is the interesting part:

```bash
go test -v -run 'Test_fileSystem$' ./...
```

If the tests fail with `expired_access_token`, generate a new token. If they
fail with `missing_scope`, the app is missing one of the scopes from the
table above — add it and generate a new token afterwards.

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
