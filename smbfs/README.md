# SMB File System

A [go-fs](https://github.com/ungerik/go-fs) file system for SMB2/3 shares —
Windows shares, Samba servers and NAS devices — built on the pure Go
[github.com/cloudsoda/go-smb2](https://github.com/cloudsoda/go-smb2), the
maintained fork of hirochachacha/go-smb2, so it needs no mounted share and
works on every platform.

## Installation

```bash
go get github.com/ungerik/go-fs/smbfs
```

## Usage

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/smbfs"
)

func main() {
    ctx := context.Background()

    free, err := smbfs.EnsureRegistered(
        ctx,
        "smb://alice@nas.local/documents", // [smb://][user[:password]@]host[:port]/share
        &smbfs.Options{Password: "secret", Domain: "WORKGROUP"},
    )
    if err != nil {
        panic(err)
    }
    defer free()

    // Use it like any other go-fs file system
    file := fs.File("smb://alice@nas.local/documents/notes.txt")

    err = file.WriteAllString(ctx, "Hello, SMB!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.pdf")
}
```

- `Dial(ctx, address, opts)` returns a file system without registering it.
- `DialAndRegister(ctx, address, opts)` registers it so `fs.File` values
  with its prefix work, and returns only an error. It fails with
  `fs.ErrAlreadyRegistered` if the prefix is taken.
- `EnsureRegistered(ctx, address, opts)` shares one file system per URI
  prefix between callers with reference counting and returns a `free`
  function instead of `Close`.

The default port is 445. File URIs have the form
`smb://user@host:port/share/path`; the port is omitted if it is 445, and
`smb://user@host:445/share/path` is accepted as well. URIs, `FileInfo.File`,
error messages and `String()` never contain the password.
A `/`, `?` or `#` in the username or password of an address must be
percent-encoded (`%2F`, `%3F`, `%23`), and so must an `@` in the share
(`%40`): an address with an `@` after the host is rejected, because that
`@` can end a password with an unencoded `/`.
`smbFS.ID()` is the prefix without the scheme.

### Authentication

`Options.Username` and `Options.Password` are used for NTLMv2
authentication and override the credentials of the address;
`Options.Password` is also used together with a username of the address,
as in the example above. `Options.Domain` and `Options.Workstation` are the
optional NTLM parameters.

Without a username and password the session is anonymous (an NTLM null
session), and the URIs have the form `smb://host/share/path`. Servers allow
that only for shares with guest access; Samba maps it to its guest account,
some other servers need the username `Guest` instead.

```go
&smbfs.Options{
    Username:    "alice",
    Password:    "secret",
    Domain:      "CORP",
    Timeout:     10 * time.Second, // TCP connect, negotiation, authentication and mount
    DialContext: proxyDialer.DialContext,
}
```

### Connections, reconnects and cancellation

A file system keeps one SMB session that operations use concurrently.
When its connection broke, for example because a Windows server
disconnected the session after its idle timeout of about 15 minutes, the
next operation dials a new session before it sends its request. An
operation that fails because the connection broke while a request was on
the way is retried once on a new session, unless the request changes the
share: if the connection broke after `Remove`, `MakeDir` or
`CreateSymbolicLink` sent their request, or `Move` its rename after the
checks of source and destination, they return the connection error ("may
have been executed"), because the server may have executed it. Operations
waiting for a reconnect share its result, also when the dial timed out
after `Options.Timeout`. Open file handles belong to their session and fail
after a reconnect.

Operations with a `context.Context` are aborted when the context is done.
`Close` unregisters the file system, aborts running operations, reconnects
and the requests of open file handles, and afterwards every method returns
`fs.ErrFileSystemClosed`.

## How SMB concepts map to the file system

- **Paths.** Paths below the share, separator `/`. The share itself is the
  root directory of the file system. A path or symbolic link target with a
  backslash is an error wrapping `os.ErrInvalid`: SMB servers take the
  backslash for a separator and resolve `..`, so a name like `..\x` from a
  `SubFileSystem` or `CopyRecursive` would reach a file outside of its
  directory.
- **Native operations.** SMB has real directories and random access file
  handles, so nearly every optional interface is native: `ReadAll`,
  `WriteAll`, `OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `Touch`,
  `MakeAllDirs`, `RemoveAll`, `Move`, `SetPermissions`, symbolic links and
  seeking reads. The rest uses the generic emulation of the `fs` package. File
  handles behave like `os.File`: `ReadAt` returns `io.EOF` at the end of the
  file and `Seek` to a negative offset fails. After `Close` of the file system
  their read and write methods, and `Seek` relative to the end, return errors
  wrapping `fs.ErrFileSystemClosed`, also through `io.Copy` and `io.WriteString`, and
  their `Close` returns nil.
- **Move.** The SMB rename replaces any existing destination, so `Move`
  checks the destination first: an existing directory is an error wrapping
  `os.ErrExist`, an existing file is replaced, a directory can't replace a
  file (`fs.ErrIsNotDirectory`), and a directory can't be moved into itself
  (`os.ErrInvalid`). A symbolic link is moved as link; moving it onto the
  file it points to is an error wrapping `os.ErrInvalid` ("are the same
  file", like `mv`), because the rename would replace the file with a link
  to itself. SMB reports no link count here, so a move onto another hard
  link of that file is refused too. Servers that follow links themselves,
  like Samba without the SMB3 POSIX extensions, show a link as the file it
  points to, and the server decides about the rename.
- **Symbolic links.** `CreateSymbolicLink` stores the target as given and
  `ReadSymbolicLink` returns it as stored. A relative target that SMB would
  store as an absolute path is an error wrapping `os.ErrInvalid`: one that
  starts with a drive like `C:x` or `C:/x` (go-smb2 stores `\??\C:x`), also
  after `./` elements, and one like `.//x`, which go-smb2 cleans to `\x`;
  so are `.` and `./`, which go-smb2 can't store. A `SubFileSystem` stores
  relative targets verbatim, such a link would point out of its directory.
  Most servers reject symbolic links over SMB (Samba without unix
  extensions, Windows without the privilege), which is an `fs.ErrUnsupported`.
- **Permissions.** SMB stores a read-only attribute, not Unix permission
  bits, so only the user write bit round-trips through `SetPermissions`.
- **Errors.** Missing files wrap `os.ErrNotExist`, `MakeDir` on an existing
  path wraps `os.ErrExist`, access denied wraps `os.ErrPermission`, reading
  or writing a directory is an `fs.ErrIsDirectory`, removing a non-empty
  directory is an `fs.ErrDirectoryNotEmpty`, `Remove` and `RemoveAll` of the
  root directory are errors wrapping `os.ErrInvalid`, and after `Close` every
  method returns `fs.ErrFileSystemClosed`, also `Remove` and `RemoveAll` of
  the root directory.

## Testing

The tests run the go-fs conformance suite and the reconnect tests against a
Samba server in a Docker container (`dperson/samba`) with a share for a test
user and a share with guest access, both on tmpfs together with the session
databases of smbd, so a full Docker disk doesn't fail the tests. These
tests are skipped when Docker is unavailable, meaning the `docker` command
is not installed or its daemon doesn't answer `docker info`. When Docker is
available but the container can't be started, the test run fails instead of
skipping, except on Windows, where Docker usually can't run the Linux image
and the tests are skipped. The other tests run offline.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
