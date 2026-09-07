# SMB File System

A [go-fs](https://github.com/ungerik/go-fs) file system for SMB2/3 shares —
Windows shares, Samba servers and NAS devices — built on the pure Go
[go-smb2](https://github.com/hirochachacha/go-smb2), so it needs no
mounted share and works on every platform.

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

    smbFS, err := smbfs.DialAndRegister(
        ctx,
        "smb://alice@nas.local/documents", // [smb://][user[:password]@]host[:port]/share
        &smbfs.Options{Password: "secret", Domain: "WORKGROUP"},
    )
    if err != nil {
        panic(err)
    }
    defer smbFS.Close()

    // Use it like any other go-fs file system
    file := fs.File("smb://alice@nas.local/documents/notes.txt")

    err = file.WriteAllString(ctx, "Hello, SMB!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.pdf")
}
```

`Dial` returns a file system without registering it, `DialAndRegister`
registers it so `fs.File` values with its prefix work.

The default port is 445. File URIs have the form
`smb://user@host/share/path`, or `smb://host/share/path` without a user.
`smbFS.ID()` is that prefix without the scheme.

### Authentication

`Options.Username` and `Options.Password` are used for NTLMv2
authentication and override credentials embedded in the address.
`Options.Domain` and `Options.Workstation` are the optional NTLM
parameters, and `Options.Dialer` replaces the `net.Dialer` used for the TCP
connection:

```go
&smbfs.Options{
    Username: "alice",
    Password: "secret",
    Domain:   "CORP",
    Dialer:   &net.Dialer{Timeout: 10 * time.Second},
}
```

## How SMB concepts map to the file system

- **Paths.** Paths below the share, separator `/`. The share itself is the
  root directory of the file system.
- **Native operations.** SMB has real directories and random access file
  handles, so nearly every optional interface is native: `ReadAll`,
  `WriteAll`, `OpenAppendWriter`, `OpenReadWriter`, `Truncate`, `Touch`,
  `MakeAllDirs`, `RemoveAll`, `Move`, `SetPermissions`, symbolic links and
  seeking reads. The rest uses the generic emulation of the `fs` package.
- **Permissions.** SMB stores a read-only attribute, not Unix permission
  bits, so only the user write bit round-trips through `SetPermissions`.
- **Errors.** Missing files wrap `os.ErrNotExist`, `MakeDir` on an existing
  path wraps `os.ErrExist`, access denied wraps `os.ErrPermission`, and
  after `Close` every method returns `fs.ErrFileSystemClosed`.

## Testing

The tests run the go-fs conformance suite against a Samba server in a
Docker container (`dperson/samba`). Docker is required; if it is not
installed the tests are skipped, if it is installed but the container can't
be started the tests fail.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
