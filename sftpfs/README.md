# SFTP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for SFTP servers,
built on [github.com/pkg/sftp](https://github.com/pkg/sftp) and
`golang.org/x/crypto/ssh`.

## Installation

```bash
go get github.com/ungerik/go-fs/sftpfs
```

## Usage

```go
import (
    "context"

    "golang.org/x/crypto/ssh/knownhosts"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/sftpfs"
)

func main() {
    ctx := context.Background()

    sftpFS, err := sftpfs.DialAndRegister(
        ctx,
        "sftp://user@example.com",              // port 22 if not in the address
        sftpfs.Password("secret"),              // or UsernameAndPassword
        knownhosts.New("~/.ssh/known_hosts"),   // ssh.HostKeyCallback
        nil,                                    // optional fs.Logger for connection events
    )
    if err != nil {
        panic(err)
    }
    defer sftpFS.Close()

    // Use it like any other go-fs file system
    file := fs.File("sftp://user@example.com/home/user/notes.txt")

    err = file.WriteAllString(ctx, "Hello, SFTP!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

`Dial` returns a file system without registering it, `DialAndRegister`
registers it so `fs.File` values with its prefix work, and
`EnsureRegistered` shares one connection per address between callers with
reference counting and returns a `free` function instead of `Close`.

File URIs have the form `sftp://user@host/path`; `:22` is accepted and
trimmed, other ports stay part of the prefix. `sftpFS.ID()` is the URI
prefix of the connection (`sftp://user@host`).

### Host keys

The `hostKeyCallback` argument is a standard `ssh.HostKeyCallback`. Use
`knownhosts.New` in production; `sftpfs.AcceptAnyHostKey` skips the check
and is only meant for tests.

For URIs with embedded credentials (`sftp://user:password@host/path`) the
package registers a file system under the plain `sftp://` prefix that dials
a connection per operation. It verifies host keys with the package variable
`sftpfs.URLHostKeyCallback`, which must be set before such a URI is used.

### Connections

A dialed file system keeps one SFTP connection. If it breaks, the next
operation reconnects with the stored credentials and host key callback and
is retried once. A single dial retries connection errors up to
`sftpfs.MaxConnectRetries` (3) times with exponential backoff starting at
`sftpfs.InitialRetryBackoff`.

## How SFTP concepts map to the file system

- **Paths.** SFTP paths are the server's absolute paths, separator `/`.
- **Native operations.** Real directories and random access file handles,
  so `OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `Touch`,
  `MakeAllDirs`, `RemoveAll`, `ListDirRecursive`, `Move`,
  `SetPermissions` and symbolic links are native; everything else uses the
  generic emulation of the `fs` package.
- **Permissions.** Unix permission bits, set with `SetPermissions`.
- **Errors.** Missing files wrap `os.ErrNotExist`, `MakeDir` on an existing
  path wraps `os.ErrExist`, and after `Close` every method returns
  `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against an OpenSSH server in a
Docker container built from `Dockerfile.sftp-test`. Docker is required; if
it is not installed the tests are skipped, if it is installed but the
container can't be started the tests fail.

Tests against public internet SFTP servers only run with
`GOFS_ONLINE_TESTS=1`.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
