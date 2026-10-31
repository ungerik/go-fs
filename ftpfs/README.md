# FTP File System

A [go-fs](https://github.com/ungerik/go-fs) file system for FTP and FTPS
servers, built on [github.com/jlaffaye/ftp](https://github.com/jlaffaye/ftp).

## Installation

Get the backend together with the go-fs core module, at the same version:

```bash
go get github.com/ungerik/go-fs@v1.0.0-rc.2 github.com/ungerik/go-fs/ftpfs@v1.0.0-rc.2
```

## Usage

```go
import (
    "context"

    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/ftpfs"
)

func main() {
    ctx := context.Background()

    free, err := ftpfs.EnsureRegistered(
        ctx,
        "ftps://user@example.com",            // [ftp[s]://][user[:password]@]host[:port]
        &ftpfs.Options{Password: "secret"}, // nil uses the defaults
    )
    if err != nil {
        panic(err)
    }
    defer free()

    // Use it like any other go-fs file system
    file := fs.File("ftps://user@example.com/pub/notes.txt")

    err = file.WriteAllString(ctx, "Hello, FTP!")
    content, err := file.ReadAllString(ctx)
    files, err := file.Dir().ListDirMax(ctx, -1, "*.txt")
}
```

- `Dial(ctx, address, opts)` returns a file system without registering it.
- `DialAndRegister(ctx, address, opts)` registers it so `fs.File` values
  with its prefix work, and returns only an error. It fails with
  `fs.ErrAlreadyRegistered` if the prefix is taken.
- `EnsureRegistered(ctx, address, opts)` shares one file system per URI
  prefix between callers with reference counting and returns a `free`
  function instead of `Close`.

File URIs have the form `ftp://user@host:port/path` or
`ftps://user@host:port/path`. The port is omitted if it is 21, and
`ftp://user@host:21/path` is accepted as well. `ftpFS.ID()` is the URI
prefix of the connection (`ftp://user@host`). URIs, `FileInfo.File`,
error messages and `String()` never contain the password. There is no file
system for the bare `ftp://` and `ftps://` prefixes, so a URI with a
password like `ftp://user:secret@host/path` doesn't resolve: dial the
address `ftp://user:secret@host` and use URIs without the password.

A `/`, `?` or `#` in the username or password of an address must be
percent-encoded (`%2F`, `%3F`, `%23`). A path in the address is ignored,
but an `@` in it must be percent-encoded too (`%40`): an address with an
`@` after the host is rejected, because that `@` can end a password with
an unencoded `/`.

### Credentials

`Options.Username` and `Options.Password` override the user info of the
address. `Options.Credentials` is a `CredentialsCallback` that is called
instead by every `Dial`, `DialAndRegister` and `EnsureRegistered` call, for
every reconnect, for the dedicated connection of every `OpenReader` and for
the connections that `Remove` and `RemoveAll` dial for `LIST`, so it should
be cheap or cache its result, and return the same user every time: the
connections for `LIST` resolve relative paths against the login directory of
their user and must see the same files as the connection of the operation.
`ftpfs.Password` and `ftpfs.UsernameAndPassword` are simple callbacks.
Without a username the login is anonymous as defined for `ftp://` URLs by
RFC 1738: the user `anonymous` with `Options.Password` (customarily an e-mail
address) or `anonymous`, and the URI prefix has no user
(`ftp://example.com`).

Paths and credentials with CR, LF or NUL characters are rejected with an
error wrapping `os.ErrInvalid`, because they would inject FTP commands.

### TLS

`ftp://` is plain FTP. `ftps://` uses explicit TLS (`AUTH TLS`) on port 21
by default and implicit TLS when the port is 990. The server certificate is
verified for the dialed host unless `Options.InsecureSkipVerify` is set.
The data connections resume the TLS session of the control connection,
which servers like vsftpd (`require_ssl_reuse`) and FileZilla Server
require; a `ClientSessionCache` is added to a `TLSConfig` without one.

```go
ftpFS, err := ftpfs.Dial(ctx, "ftps://user@example.com", &ftpfs.Options{
    Password:    "secret",
    TLSConfig:   &tls.Config{MinVersion: tls.VersionTLS13},
    Timeout:     10 * time.Second, // dial, TLS handshake, greeting and login
    DialContext: proxyDialer.DialContext,
    DebugOut:    os.Stderr, // FTP protocol log, the password is replaced by ****
})
```

### Data connections

Data connections use EPSV (RFC 2428) and fall back to PASV if the server
rejects it; `Options.DisableEPSV` uses PASV right away. Either way they go
to the host of the control connection with the port of the server's reply,
never to the address of a PASV reply, which could point to another or an
internal host.

### Connections, retries and cancellation

A dialed file system keeps one control connection that is used by one
operation at a time. If it breaks, the next operation reconnects. An operation
that failed because the connection broke is retried once on a new connection,
but only if nothing was changed on the server yet: after an `APPE`, `DELE`,
`MKD`, `RMD` or `RNTO` command was sent, the operation returns the connection
error ("may have been executed") instead of running the command twice, because
only its reply may have been lost. `RemoveAll` is retried, because repeating
it removes nothing that should be kept. Before such a command, an idle
connection is checked with `NOOP`, so a connection that the server closed
while it was idle is replaced first.

`OpenReader` dials a dedicated connection for the transfer, so a streaming
read never blocks other operations. `RemoveAll` of a directory on a server
with MLST support, and `Remove` and `RemoveAll` of a path that MLST reports
as missing if `DELE` fails, dial a second connection for `LIST` while they
run. It follows the context of the operation and `Close`, and if it breaks,
like when the server closed it while it was idle, it is dialed again once
for the listing, see **Symbolic links** below.

Operations with a `context.Context` are aborted when the context is done.
`Close` unregisters the file system, aborts running operations, reconnects
and open readers, and afterwards every method returns
`fs.ErrFileSystemClosed`.

## How FTP concepts map to the file system

- **Paths.** The server's paths, separator `/`. Listings are parsed by the FTP
  client library, so what a server reports about modification times and sizes
  is what the `FileInfo` contains. A path with a backslash is an error
  wrapping `os.ErrInvalid` unless `Options.AllowBackslashInNames` is set: for
  the separator `/` the backslash is part of a name, but a server on Windows
  (IIS, FileZilla Server) takes it for a separator and resolves `..`, so a
  name like `..\x` from a `SubFileSystem` or `CopyRecursive` would reach a
  file outside of its directory. Set the option only for servers that store
  backslashes in names, like servers on Unix. A path with a CR, LF or NUL
  character, which would inject another FTP command, is an error wrapping
  `os.ErrInvalid` too. The file system implements `fs.ValidateNameFileSystem`
  with these rules and rejects new names that are not valid UTF-8, which
  servers in the UTF8 mode of RFC 2640, which the client turns on, expect,
  so a copy or move into it fails before it writes anything.
- **Native operations.** `ReadAll`, `WriteAll`, `Append`,
  `OpenAppendWriter`, `OpenReadWriter`, `Touch`, `RemoveAll`,
  `ListDirRecursive` and `Move` are native; everything else uses the generic
  emulation of the `fs` package. Writers buffer the data in memory and
  store it with one `STOR` or `APPE` in `Close`; they implement
  `fs.WriteAborter`.
- **Move.** Servers differ in what a rename onto an existing path does, so
  `Move` checks the destination first: an existing directory is an error
  wrapping `os.ErrExist`, an existing file is replaced, a directory can't
  replace a file (`fs.ErrIsNotDirectory`), and a directory can't be moved
  into itself (`os.ErrInvalid`). If the server refuses to rename onto an
  existing file, that file is renamed to a temporary `.gofs-replaced-*` name
  in its directory and only deleted after the source took its place, or
  renamed back if the rename fails again. An error that leaves it at the
  temporary name contains that name.
- **Permissions.** FTP has no portable permission model. The reported
  permissions are the constants `ftpfs.DefaultPermissions` /
  `ftpfs.DefaultDirPermissions`, and `SetPermissions` is not supported.
- **Symbolic links.** Servers without MLST support list links like
  `ls -l` with their target. `Stat` follows such a link like `os.Stat`:
  it resolves the listed target relative to the directory of the link and
  describes the file or directory it points to, with `IsSymlink` set; a
  dangling link wraps `os.ErrNotExist`. `ListDir` and `ListDirRecursive`
  describe a link itself, with `IsSymlink` set and neither `IsDir` nor
  `IsRegular`, `ListDirRecursive` follows only a link at the listed path,
  and `Remove`, `RemoveAll` and `Move` act on the link itself. The FTP
  client library cuts the line of a link at the first ` -> ` into its name
  and its target, so `ListDir` and `ListDirRecursive` name the link
  `a -> b` `a`, and `RemoveAll` of a directory with such a link fails.
  `Stat`, `Remove`, `RemoveAll` and `Move` find the link `a -> b`, but they
  also take the link `a` to `b -> c` for a link `a -> b` to `c`, because
  both have the same line. Servers
  among them that expand glob characters in the path argument of `LIST`,
  like wu-ftpd and ProFTPD with `UseGlobbing` but without `mod_facts`
  (which provides MLST), can list another directory for a path with `*`,
  `?` or `[`: then `ftpfs` describes the entries of that directory, and
  `RemoveAll` can take a link to a directory for a directory in it and
  delete the files that the link points to. `RemoveAll` refuses a directory
  whose name its listing lists more than once, like for a file whose name
  contains a line feed followed by a forged line. Servers among them that
  hide the names that begin with a dot from `LIST`, like vsftpd with
  `force_dot_files=NO` (its default), hide them from `ftpfs` too: `Stat`
  reports such a file as missing, `ListDir`, `ListDirRecursive` and copies
  skip it, and `RemoveAll` of its directory fails, and a link hidden that way
  doesn't count against a forged line with its name, so `RemoveAll` can
  delete the files it points to: set `force_dot_files=YES` on such servers.
  A server
  whose LIST lists a link itself like `ls -l` instead of the directory it
  points to is supported: `ListDir` and `ListDirRecursive` of a link to a
  directory list the directory. Servers with MLST support describe a link
  with the facts of its target or with a type that the FTP client library
  reads as a file, so their links look like files and directories:
  `ListDirRecursive` lists the files below a link to a directory.
  `RemoveAll` still deletes such a link itself, because it tries `DELE`
  for a directory before it deletes its content. If `DELE` fails, like for
  a link that the server refuses to delete, `RemoveAll` deletes the content
  only if `LIST` of the parent directory, which these servers answer like
  `ls -l`, lists the path once and as a directory, and returns an error
  otherwise; the FTP client library lists with `MLSD` on these servers, so
  `RemoveAll` of a directory dials a second connection for `LIST`. For
  every directory it changes to the parent directory with `CWD` and sends
  `LIST` without a path argument, because servers like ProFTPD with
  `UseGlobbing` and Pure-FTPd expand glob characters like `[` in that
  argument. No listing is kept for the next directory, so a directory that
  another client replaced with a link while `RemoveAll` removed the
  directories next to it is not deleted through; a replacement in the time
  from the `LIST` until the content is deleted, at least one round trip,
  goes unnoticed. The client
  library sends `LIST -a`, and servers that don't parse the options of
  `LIST` take `-a` for a path and list the entry `-a` of the directory
  instead, so `RemoveAll` doesn't use the listing of a directory that has an
  entry named `-a`, which it checks with `MLST`, and fails for the
  directories in such a directory. The client library drops a `LIST` line
  that it can't parse without an error, like one whose year doesn't have
  four digits, so a client that can write into the directory and set the
  modification time of its link can hide the link's line and, with a file
  whose name contains a line feed followed by a forged directory line, make
  `RemoveAll` delete through the link. FTP servers on Windows (IIS with
  listings in the DOS format, and FileZilla Server or IIS with listings in
  the Unix format, which list junctions and links to directories as
  directories) and ProFTPD with `ShowSymlinks off` list a link to a
  directory as a directory with `LIST` too, so `RemoveAll` can't tell them
  apart there and deletes the files that such a link points to, as before
  v1.0. `Remove` of a link to a directory fails like for a
  directory and deletes nothing, and `Move` onto such a link is refused
  with an error wrapping `os.ErrExist`. A dangling link looks missing to
  `Stat`, because MLST follows links, but `Remove` and `RemoveAll` delete
  it and `Move` renames it. If the server refuses to delete it, `Remove`
  and `RemoveAll` return the error of `DELE` if `LIST` of the parent
  directory lists the path, and report it as missing otherwise, also if the
  directory can't be listed, unlike the directory check of `RemoveAll`,
  which fails without a listing. FTP has no command to create a link, so
  `ftpfs` doesn't implement `fs.SymbolicLinkFileSystem`:
  `fs.CopyRecursive` copies a link to a file as a file and fails with an
  error wrapping `errors.ErrUnsupported` for a link to a directory before
  it writes anything.
- **Errors.** Missing files wrap `os.ErrNotExist`, reading or writing a
  directory is an `fs.ErrIsDirectory`, `MakeDir` on an existing path wraps
  `os.ErrExist`, removing a non-empty directory is an
  `fs.ErrDirectoryNotEmpty`, and after `Close` every method returns
  `fs.ErrFileSystemClosed`.

## Testing

The tests, including the go-fs conformance suite over FTP, over FTPS with
TLS session reuse, against a server without MLST and against servers that
list the `.` and `..` entries, run against an in-process FTP server with
failure hooks, so they need neither Docker nor a network.

Tests against public internet FTP servers only run with
`GOFS_ONLINE_TESTS=1`.

```bash
go test ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
