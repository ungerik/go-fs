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
    "os"
    "path/filepath"

    "golang.org/x/crypto/ssh/knownhosts"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/sftpfs"
)

func main() {
    ctx := context.Background()

    // knownhosts.New returns the callback and an error, and it does not expand "~"
    home, err := os.UserHomeDir()
    if err != nil {
        panic(err)
    }
    hostKeyCallback, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
    if err != nil {
        panic(err)
    }

    sftpFS, err := sftpfs.Dial(ctx, "sftp://user@example.com", &sftpfs.Options{
        Password:        os.Getenv("SFTP_PASSWORD"),
        HostKeyCallback: hostKeyCallback, // required
    })
    if err != nil {
        panic(err)
    }
    defer sftpFS.Close()

    // Register it so that File URIs with its prefix work
    if err := fs.Register(sftpFS); err != nil {
        panic(err)
    }
    file := fs.File("sftp://user@example.com/home/user/notes.txt")

    err = file.WriteAllString(ctx, "Hello, SFTP!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

`Dial` returns a file system without registering it, `DialAndRegister`
registers it and returns only an error (close it later with
`fs.GetFileSystemByPrefixOrNil("sftp://user@example.com").Close()`), and
`EnsureRegistered` shares one connection per prefix between callers with
reference counting and returns a `free` function instead:

```go
free, err := sftpfs.EnsureRegistered(ctx, "sftp://user@example.com", opts)
if err != nil {
    return err
}
defer free() // the last free closes the connection
```

The address is `host`, `host:port` or `user@host:port`, with or without the
`sftp://` scheme. IPv6 addresses are written in brackets (`[::1]`,
`sftp://user@[::1]:2222`). The port defaults to 22. A path in the address is
ignored. A `/`, `?` or `#` in the username or password must be
percent-encoded (`%2F`, `%3F`, `%23`), and so must an `@` in the path
(`%40`): an address with an `@` after the host is rejected, because that
`@` can end a password with an unencoded `/`.

File URIs have the form `sftp://user@host/path`; `:22` is accepted and
trimmed, other ports stay part of the prefix. `sftpFS.ID()` is the URI
prefix of the connection (`sftp://user@host`), `sftpFS.String()` is
`SFTP sftp://user@host`.

### Options

| Field | Meaning | Zero value |
|---|---|---|
| `Username`, `Password` | SSH login; empty values are taken from the address | |
| `Credentials` | callback for the username and password, called when the file system is dialed and again before every reconnect (rotated secrets) | not used |
| `Auth` | `ssh.AuthMethod`s tried before the password: `ssh.PublicKeys`, an SSH agent via `ssh.PublicKeysCallback`, `ssh.KeyboardInteractive` | none |
| `HostKeyCallback` | verifies the server, **required** | dialing fails |
| `HostKeyAlgorithms` | accepted host key algorithms in order of preference, like `ssh.KeyAlgoED25519`; set it if the callback knows only some keys of the server, see [Host keys](#host-keys) | defaults of `x/crypto/ssh` |
| `Logger` | logs dials, retries, lost connections and reconnects | no logging |
| `ConnectRetries` | retries of a failed connection attempt, so a dial makes up to 1+N attempts; only network errors and timeouts are retried, not authentication or host key errors | `DefaultConnectRetries` (3), negative: no retries |
| `RetryBackoff` | wait before the first retry, doubled for every further retry | `DefaultRetryBackoff` (100 ms) |
| `Timeout` | limit of every connection attempt: TCP connect, SSH handshake and start of the SFTP session | `DefaultTimeout` (30 s), negative: none |
| `KeepAlive` | interval of SSH keep-alive requests, at most one outstanding; a connection that leaves `KeepAliveCountMax` (3) intervals in a row without an answer is closed, a slower answer keeps it | `DefaultKeepAlive` (30 s), negative: off |
| `DialContext` | opens the network connection, for example through a proxy | `net.Dialer` |

### Credentials in URIs

The password of an address like `sftp://user:password@host/path` is used to
dial, so such a URI can be passed to `EnsureRegistered`. The prefix of the
file system never contains the password: files are addressed as
`sftp://user@host/path`, and passwords never appear in errors, log
messages, `FileInfo.File` or `String()`. There is no file system for the
plain `sftp://` scheme anymore that dials a connection per operation.

### Host keys

`Options.HostKeyCallback` is a standard `ssh.HostKeyCallback` and required.
Use `knownhosts.New` or `ssh.FixedHostKey` in production;
`sftpfs.AcceptAnyHostKey` skips the check and is only meant for tests.

`x/crypto/ssh` asks the server for an ECDSA or RSA host key before an
ED25519 key. If the callback only knows the ED25519 key of a server that
has an ECDSA key too, like a `known_hosts` file with only that key, the dial
fails with `ssh: host key mismatch`
([golang/go#29286](https://github.com/golang/go/issues/29286)). Set
`Options.HostKeyAlgorithms` to the algorithms of the known keys, for example
`[]string{ssh.KeyAlgoED25519}`, or take them from the `HostKeyAlgorithms`
method of [github.com/skeema/knownhosts](https://github.com/skeema/knownhosts).

### Connections

A file system keeps one SSH connection with one SFTP session. `Close`
closes the SSH connection, ends a reconnect in progress and unregisters the
file system; afterwards every method returns `fs.ErrFileSystemClosed`.

If the connection is lost, the next operation reconnects with the stored
options. A reconnect runs without holding a lock, so a server that does not
answer blocks neither `Close` nor operations, which stop waiting when their
context ends. A canceled or timed out context of an operation never drops
the connection, and neither does a status reply of the server, whatever its
message says: the reply proves that the connection works. `io.EOF`, which
`pkg/sftp` returns for a closed channel and for an `SSH_FX_EOF` reply, only
drops the connection if its SFTP session ended or it does not answer a
keep-alive request.

An operation that fails because the connection was lost while it ran is
retried once on a new connection if repeating it can't change the result:
`Stat`, `Lstat`, `ReadSymbolicLink`, `OpenReader`, `Truncate`,
`SetPermissions`, `MakeAllDirs`, `RemoveAll`. Operations that change the
server in a way that can't be repeated (`Remove`, `Move`, `MakeDir`,
`CreateSymbolicLink`, `Touch` of a new file, opening a writer) are only
retried if the connection was lost before their request was sent; else
they return the connection error, although the server may have executed
the request. `ListDir` and `ListDirRecursive` are never retried after
they called the callback, and the errors of the callback are returned as
they are.

## How SFTP concepts map to the file system

- **Paths.** SFTP paths are the server's absolute paths, separator `/`.
- **Native operations.** Real directories and random access file handles,
  so `OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `Touch`,
  `MakeAllDirs`, `RemoveAll`, `ListDirRecursive`, `Move`,
  `SetPermissions` and symbolic links are native; everything else uses the
  generic emulation of the `fs` package. Append writers start at the end
  of the file, also on servers that write at the offset the client sends.
- **Move.** An existing file at the destination is replaced, atomically
  with the `posix-rename@openssh.com` extension (OpenSSH). Without the
  extension the destination file is removed before the rename, so for a
  moment neither file exists there, and if the rename fails the destination
  is gone. If the destination may be the source under another name, like
  for a case-only rename on a case-insensitive server or a path through a
  symbolic link to the directory of the source, the source is renamed to a
  temporary name in the destination directory first, the destination is
  only removed if it still exists then, and the source is renamed back if a
  later step fails. An existing directory at the destination is an error
  wrapping `os.ErrExist`, a directory onto a file is an
  `fs.ErrIsNotDirectory`, a directory into itself an error wrapping
  `os.ErrInvalid`.
- **Recursive operations.** `ListDirRecursive` of a symbolic link to a
  directory lists the files of that directory below the path of the link,
  symbolic links in the tree are not followed. `Remove` and `RemoveAll`
  refuse the root directory `/` with an error wrapping `os.ErrInvalid`
  without a request to the server.
- **Permissions.** Unix permission bits, set with `SetPermissions`.
- **Errors.** Missing files wrap `os.ErrNotExist` (`fs.ErrDoesNotExist`
  with the file URI), `MakeDir` on an existing path wraps `os.ErrExist`,
  reading or writing a directory is an `fs.ErrIsDirectory`, `Remove` of a
  non-empty directory an `fs.ErrDirectoryNotEmpty`.

## Testing

Most tests run offline against an in-process SSH server with the SFTP
servers of `pkg/sftp`, including the go-fs conformance suite on a
temporary directory and tests that lose the connection at chosen requests.

The conformance suite also runs against an OpenSSH server in a Docker
container built from `Dockerfile.sftp-test`. If Docker is not installed
these tests are skipped, if it is installed but the container can't be
started the tests fail.

Tests against public internet SFTP servers only run with
`GOFS_ONLINE_TESTS=1`.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
