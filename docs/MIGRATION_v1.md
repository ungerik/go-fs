# Migrating to go-fs v1.0

v1.0 changes the consumer-facing `File`, `FileReader` and `MemFile` API in a
mechanical way, and redesigns the implementer-facing `FileSystem` interface.
Nobody outside this repository implements `FileSystem`, so this guide covers
the consumer side; implementers read
[reference-filesystem-interfaces.md](reference-filesystem-interfaces.md) and
[tutorial-implement-a-filesystem.md](tutorial-implement-a-filesystem.md).

Code that already uses v1.0.0-beta.1 reads
[Upgrading from v1.0.0-beta.1](#upgrading-from-v100-beta1); the rest of the
guide describes the way from v0.1 to the final v1 API.

## Upgrading from v1.0.0-beta.1

Almost every change below breaks compilation, so the compiler lists the call
sites. The exceptions are marked as behaviour changes.

### Constructors and registration

`New…`/`Dial…` return the file system and never register it, `…AndRegister`
registers and returns only an `error`. Temporary file systems are created,
registered and closed explicitly:

```go
memFS, err := fs.NewMemFileSystem(nil)
if err != nil {
	return err
}
if err := fs.Register(memFS); err != nil {
	return err
}
defer memFS.Close() // unregisters
```

| beta.1 | beta.2 |
| ------ | ------ |
| `fs.NewMemFileSystem(sep, files...)` (registered) | `fs.NewMemFileSystem(&fs.MemFileSystemOptions{Separator: sep}, files...)` + `fs.Register`; nil options = `/` and a random ID |
| `memFS.WithID(id)`, `memFS.WithVolume(v)` | `MemFileSystemOptions.ID`, `.Volume`; `fs.NewMemFileSystemAndRegister(opts, files...) error` needs an ID |
| `fs.NewSingleMemFileSystem(file)` | `fs.NewMemFileSystem(nil, file)` + `fs.Register` + `memFS.RootDir().Join(file.FileName)` |
| `fs.NewStdFileSystemAndRegister(fsys, id) *StdFileSystem` | `… error`, needs an id; or `NewStdFileSystem` + `fs.Register` |
| `fs.NewSubFileSystemAndRegister`, `fs.NewOverlayFileSystemAndRegister` returning the file system | return only `error` and need an id |
| `zipfs.NewReader(file)`, `zipfs.NewWriter(file)`, same for `tarfs` (registered) | `NewReader(file, id)`, `NewWriter(file, id)` (`""` = random id) + `fs.Register` |
| `multipartfs.FromRequestForm(r, maxMemory)`, `multipartfs.New(form)` (registered) | `multipartfs.NewFromRequest(r, maxMemory, id)`, `multipartfs.New(form, id) (…, error)` + `fs.Register` |
| `httpfs.FileSystem`, `httpfs.FileSystemTLS`, `httpfs.Client` | `httpfs.Default`, `httpfs.DefaultTLS`; `httpfs.New(prefix, &httpfs.Options{Client: c})` / `NewAndRegister` for a prefix like `https://api.example.com/` |
| `s3fs.NewAndRegister(client, bucket, readOnly) fs.FileSystem` | `s3fs.New(client, bucket, &s3fs.Options{ReadOnly: readOnly})` + `fs.Register`, or `NewAndRegister(…) error` |
| `s3fs.NewLoadDefaultConfig(ctx, bucket, readOnly)` (registered) | `s3fs.NewLoadDefaultConfig(ctx, bucket, opts)` + `fs.Register` |
| `s3fs.MultipartUploadThreshold`, `MultipartDownloadThreshold` | `s3fs.Options` fields (default 16 MiB) |
| `azureblobfs.New/NewAndRegister(ctx, client, readOnly)`, `NewFromConnectionString(…, readOnly)` (registered) | `(ctx, client, &azureblobfs.Options{ReadOnly: …})`; `NewFromConnectionString` doesn't register |
| `dropboxfs.NewAndRegister(ctx, token, cacheTimeout, mute) (fs.FileSystem, error)` | `dropboxfs.New(ctx, token, &dropboxfs.Options{CacheTimeout: …, Mute: …})` + `fs.Register`, or `NewAndRegister(…) error` |
| `webdavfs.NewAndRegister` returning the file system | returns only `error` |
| `sftpfs.Dial(ctx, addr, creds, hostKeyCallback, logger)` | `sftpfs.Dial(ctx, addr, &sftpfs.Options{Credentials: creds, HostKeyCallback: hk, Logger: logger})`; also `Username`, `Password`, `Auth`, `Timeout`, `KeepAlive`, `ConnectRetries`, `RetryBackoff`, `DialContext` |
| `sftpfs.MaxConnectRetries`, `InitialRetryBackoff`, `URLHostKeyCallback` | `Options.ConnectRetries`, `RetryBackoff` (0 = default, negative = off), `HostKeyCallback` |
| `ftpfs.Dial(ctx, addr, creds, opts)` | `ftpfs.Dial(ctx, addr, &ftpfs.Options{Credentials: creds, …})`, or `Username`/`Password`; also `Timeout`, `DialContext`, `DisableEPSV` |
| `smbfs.Options.Dialer` | `smbfs.Options.DialContext`, `Options.Timeout` |
| `sftpfs`/`ftpfs`/`smbfs` `DialAndRegister` returning the file system | returns only `error`; `EnsureRegistered(ctx, addr, opts)` returns `free` |

Behaviour changes:

- `fs.Register` returns an error (`fs.ErrAlreadyRegistered`) when another file
  system has the prefix, and is not reference counted any more: registering
  the same file system twice is a no-op and one `Unregister` removes it.
  Shared, reference counted file systems use `fs.EnsureRegistered` or the
  `EnsureRegistered` of the backends.
- A URI matches a prefix only at a boundary: `s3://logs` no longer serves
  `s3://logs-archive/...`.
- `fs.File("sftp://user:password@host/path")` and the same for `ftp://` no
  longer dial a connection on first use (that made every imported backend an
  SSRF risk). Call `sftpfs.EnsureRegistered(ctx, "sftp://user:password@host",
  opts)` once and use `sftp://user@host/path`.
- With a username, the webdavfs prefix is `webdav://user@host/base`, so
  stored URIs change. An `Authorization` header in `webdavfs.Options.Header`
  disables basic auth.
- ftpfs logs in as `anonymous` without a username (that was an error), uses
  EPSV by default (`Options.DisableEPSV` for PASV) and rejects paths and
  credentials containing CR, LF or NUL with `os.ErrInvalid`.
- sftpfs, ftpfs and smbfs don't retry operations that can't be repeated
  (append, remove, move, make dir, symlink, and the writers of sftpfs) after
  their request may have reached the server; they return the connection error instead.
- smbfs `ReadSymbolicLink` returns the raw target, and a Move onto an
  existing empty directory fails with `os.ErrExist` instead of replacing it.
  smbfs paths and link targets containing `\` fail with `os.ErrInvalid`
  (the server treats `\` as a separator).
- sftpfs rejects addresses with an unencoded `@` after the host, including an
  `@` in a path passed to `EnsureRegistered`: write it as `%40`, and
  percent-encode `/`, `?` and `#` in usernames and passwords. A dead
  connection is detected after `KeepAliveCountMax` (3) keep-alive intervals,
  about 90 s with the default interval. SFTP status replies, including
  `io.EOF`, reach the caller instead of dropping the connection. Set
  `Options.HostKeyAlgorithms` when the known hosts file has only an ed25519
  key of a server that also offers ECDSA.
- webdavfs no longer follows redirects to another host or port or from https
  to http; it returns a `*webdavfs.StatusError`. A `CheckRedirect` of
  `Options.Client` can allow them, then they go out without credentials.
- `Remove` and `RemoveAll` of the root directory return an error wrapping
  `os.ErrInvalid` in every file system, including the root of a local volume.
- ftpfs and smbfs reject addresses with an unencoded `@` after the host, write
  it as `%40`. webdavfs rejects a base URL without user info whose host has a
  port and whose path has a raw `@`, for example
  `https://cloud:8443/remote.php/dav/files/alice@example.com`; write
  `alice%40example.com`. A base URL with a password and an `@` after the host
  is rejected too: pass the credentials with `Options.Username` and
  `Options.Password`. smbfs rejects link targets with a drive letter.

### Errors

- The typed errors are created for a `FileReader`: `fs.NewErrDoesNotExist(file)`
  accepts a `File` or a `MemFile`. `File()` returns `(fs.File, bool)`,
  `FileReader()` the passed reader. `NewErrIsDirectory`/`NewErrIsNotDirectory`
  take a `FileReader` instead of `any`.
- Match typed errors with `errors.Is(err, fs.ErrIsDirectory{})` (any value of
  the type matches); `os.ErrNotExist`, `os.ErrExist` and `os.ErrPermission`
  still work for the types that wrap them.
- Behaviour: `fs.ErrEmptyPath` no longer wraps `os.ErrNotExist`, so
  `fs.RemoveErrDoesNotExist(fs.File("").Remove())` is an error now.
- Behaviour: a File with an unregistered prefix returns an error wrapping
  `fs.ErrInvalidFileSystem` (it said "write-only" before) that does not wrap
  `os.ErrNotExist`.
- Behaviour: removing a non-empty directory returns `fs.ErrDirectoryNotEmpty`
  on every file system.

### File, FileReader, MemFile and package functions

| beta.1 | beta.2 |
| ------ | ------ |
| `f.Rename(name)`, `f.Renamef(format, …)` | `f.Rename(ctx, name)`, `f.Renamef(ctx, format, …)` |
| `f.ReadSymbolicLink() (fs.File, error)` | `(string, error)`, the raw target |
| `f.Glob(ctx, p) (iter.Seq2[File, []string], error)`, `fs.Glob`, `MustGlob` | `iter.Seq2[fs.File, error]`; `MustGlob` removed |
| `f.WriteJSON(ctx, v, "  ")`, `WriteXML`, `MemFile.WriteJSON`, `NewMemFileWriteJSON(name, v, "  ")` | `WriteJSONIndent(ctx, v, "  ")`, `WriteXMLIndent`, `NewMemFileWriteJSONIndent`; `WriteJSON(ctx, v, perm...)` |
| `fs.ServeFileHTTP(w, r, file)`, `ServeFileHTTPHandler(file)` | `(w, r, file, "")` and `(file, "")`, `""` detects the content type |
| `fs.TempFile()`, `fs.CreateTempFile()` | `fs.TempFile("")`, `fs.CreateTempFile("")` |
| `fs.ContentHashFuncFrom(sha256.New())` | `fs.ContentHashFuncFrom(sha256.New)` |
| `fs.CopyFileBuf(ctx, src, dest, &buf)` | `fs.CopyFileBuf(ctx, src, dest, buf)` with a `[]byte` (nil allocates) |
| `fs.ReadAllContext(ctx, r)`, `fs.WriteAllContext(ctx, w, data)` | `fs.ReadAll(ctx, r)`, `fs.WriteAll(ctx, w, data)` |
| `fs.Remove(uris...)`, `fs.RemoveFile(f)` | `fs.RemoveFiles(files...)`, `fs.File.Remove` as callback |
| `fs.ReadHeader(fr, n)`, `fs.ReadHeaderString(fr, n)` | `fs.ReadHeader(ctx, fr, n)`, `fs.ReadHeaderString(ctx, fr, n)` |
| `f.URL()`, `fs.FileURLs(files)` | `f.URI()`, `fs.FileURIs(files)`: a go-fs URI escapes only `%`, it is no URL for `net/url` or HTTP clients |
| `fs.ReadSeekCloser` | `fs.ReadAtSeekCloser` |
| `f.SetXAttr(name, data, xattr.XATTR_CREATE)` | `f.SetXAttr(name, data, fs.XAttrCreate)` |
| `perm.CanUserRead()` and the other `Can…` methods | `perm.Can(fs.UserRead)` |
| `fs.Event` values from fsnotify | `fs.EventCreate`, `EventWrite`, `EventRemove`, `EventRename`, `EventChmod` |

Behaviour changes:

- `Move`, `MoveTo`, `Rename` and `CopyFile` onto an existing directory fail
  (`os.ErrExist`, `fs.ErrIsDirectory` for `CopyFile`) on every file system
  instead of merging, replacing or deleting it; an existing file is replaced.
  Moving a directory into itself fails with an error wrapping `os.ErrInvalid`.
- A failed or canceled copy aborts uploads that commit on `Close` and never
  deletes the source of a move.
- `MemFile.Stat()` has a zero `ModTime`; `MemFile.Dir()` of `"a/b/"` is `"a/"`.
- `File.Size()` is 0 for directories; `ServeFileHTTP` answers a directory
  with 404.
- `fs.CopyRecursive` patterns filter only files, sub-directories are always
  copied.
- `File.Ext()` of an HTTP URL ignores the query: `report.pdf?x=1` is `.pdf`.
- Local `OpenReader` of a directory fails with `fs.ErrIsDirectory`; a leading
  `~name` is not expanded any more (only `~` and `~/…`).
- MemFileSystem copies data in `ReadAll`/`WriteAll` and gives readers a
  snapshot; `ListDir` lists sorted by name.
- s3fs: objects written by v0 with a leading `/` in the key are not visible,
  see "Migrating from v0" in `s3fs/README.md`.
- Copies (`CopyFile`, `CopyRecursive`, moves between file systems) don't take
  over setuid, setgid and sticky bits; pass them as `perm`. A copy from a
  read-only file system gets the owner write bit, so an upload is copied as
  0644 instead of 0444.
- `FileInfo.Permissions` of Local, `StdFileSystem`, sftpfs and smbfs includes
  the setuid, setgid and sticky bits (`fs.NewFileInfo` uses
  `PermissionsFromFileMode`); mask with `0o777` to compare plain permissions.
- Recursive listings never descend into a symbolic link to a directory: the
  callback gets the link with `IsSymlink` true and `IsDir` false, like
  `filepath.WalkDir`. `CopyRecursive` and moves recreate such a link as a
  link within one file system instance and fail with
  `errors.ErrUnsupported` before writing anything between different
  instances (also a SubFileSystem and its parent); follow links yourself if
  you need the contents copied.
  A copy never writes through a symbolic link below the destination root, it
  fails with `os.ErrExist` instead.
- Moving a symbolic link onto the file it points to fails with
  `os.ErrInvalid` ("are the same file").
- `LocalFileSystem.Move` of a file with more than one hard link goes through a
  temporary `.gofs-move-*` name in the source directory.
- `fs.Zip` and `ZipMemFiles` reject names that escape with `..`, names with a
  backslash and absolute names (the leading slash of a `MemFile` name is
  dropped); `zipfs.Reader` skips symbolic links, devices and FIFOs.
- XAttr: `XAttrCreate` on an existing attribute wraps `os.ErrExist`,
  `XAttrReplace` and `GetXAttr`/`RemoveXAttr` of a missing attribute wrap
  `os.ErrNotExist`.
- Local paths: only a `~` that starts the first part of a path means the home
  directory. `CleanPath(".", "~", "x")` and `File("./~/x")` are `./~/x`, a
  directory named `~`; `CleanPath("~/..")` is the parent of the home
  directory.
- `SubFileSystem.CreateSymbolicLink` stores a relative target with `..` as an
  absolute path of the view, so `ReadSymbolicLink` returns `/d/target.txt`
  instead of `../d/target.txt`.
- `ServeFileHTTP` serves multi-range requests only from local files and
  in-memory readers; for every other reader it answers 200 with the whole
  content. A file of unknown size is streamed without `Content-Length`,
  ranges and conditional requests.
- `TempFile` returns `InvalidFile` for an extension with a path separator.
- `StdFS` of a non-local file system accepts `:` and `\` in names on
  Windows; on the local file system it, and a SubFileSystem of Local, reject
  Windows device names and NUL bytes like `os.DirFS`.
- `ParseRequestMultipartFormMemFiles` reads every uploaded file into memory,
  `maxMemory` doesn't bound that: limit the request body with
  `http.MaxBytesReader`. multipartfs ignores fields with an empty name.
- `uuiddir.Enum` follows links to directories.
- `WriteXMLIndent`, `MemFile.WriteXMLIndent` and `NewMemFileWriteXMLIndent`
  with an empty indent put every element on its own line, where beta.1's
  `WriteXML(ctx, v, "")` wrote compact XML; use `WriteXML(ctx, v)` for that.
  The JSON functions write the same as in beta.1.

### FileSystem implementations

`MoveFileSystem.Move` and `TruncateFileSystem.Truncate` take a ctx,
`SymbolicLinkFileSystem.Lstat` replaces `IsSymbolicLink`, `SetXAttr` takes
`fs.XAttrFlags`, `RenameFileSystem` and `ListDirMaxFileSystem` are removed,
`fsimpl.PathHelper` has no `IsAbsPath`/`AbsPath`/`IsHidden`,
`fsimpl.PathHelper.URL` and `LocalFileSystem.URL` are `URI`,
`fsimpl.ReadonlyFileBuffer` and its constructors are spelled `ReadOnly…`, and
`fstest.DockerAvailable`/`DockerSetupFailed` are internal. Writers that commit
on `Close` should implement `fs.WriteAborter`. Run `fstest.RunConformance`,
which checks the new contracts. Its XAttr check requires `XAttrCreate` errors
to wrap `os.ErrExist` and `XAttrReplace` and missing-attribute errors to wrap
`os.ErrNotExist`, and the suite stops when `WriteSeed` fails. The layers of an
`OverlayFileSystem` must be case-sensitive.

## The context rule

A method takes `ctx context.Context` as its first parameter only if it is a
potentially long-running read or write operation: content transfer,
directory iteration, and connect functions. Metadata and open methods do
not. The `*Context` twins are gone; where a pair existed, the ctx variant
survives under the short name.

| v0.1                                   | v1.0                                         |
| -------------------------------------- | -------------------------------------------- |
| `f.ReadAll()` / `f.ReadAllContext(ctx)` | `f.ReadAll(ctx)`                             |
| `f.ReadAllString()` / `...Context(ctx)` | `f.ReadAllString(ctx)`                       |
| `f.ContentHash()` / `...Context(ctx)`   | `f.ContentHash(ctx)`                         |
| `f.WriteAll(data)` / `...Context(ctx, data)` | `f.WriteAll(ctx, data)`                 |
| `f.WriteAllString(s)` / `...Context`    | `f.WriteAllString(ctx, s)`                   |
| `f.ListDir(cb)` / `f.ListDirContext(ctx, cb)` | `f.ListDir(ctx, cb)`                   |
| `f.ListDirIter(...)` / `...Context`     | `f.ListDirIter(ctx, ...)`                    |
| `f.ListDirInfo(cb)` / `...Context`      | `f.ListDirInfo(ctx, cb)`                     |
| `f.ListDirRecursive(cb)` / `...Context` | `f.ListDirRecursive(ctx, cb)`                |
| `f.ListDirRecursiveIter(...)`           | `f.ListDirRecursiveIter(ctx, ...)`           |
| `f.ListDirInfoRecursive(cb)`            | `f.ListDirInfoRecursive(ctx, cb)`            |
| `f.ListDirMax(n)` / `...Context(ctx, n)` | `f.ListDirMax(ctx, n)`                      |
| `f.ListDirRecursiveMax(n)`              | `f.ListDirRecursiveMax(ctx, n)`              |
| `f.Glob(p)`, `fs.Glob(p)` | `f.Glob(ctx, p)`, `fs.Glob(ctx, p)` yield `(File, error)`; `MustGlob` is removed |
| `f.Truncate(size)`                      | `f.Truncate(ctx, size)`                      |
| `f.MoveTo(dest)`                        | `f.MoveTo(ctx, dest)`                        |
| `f.Rename(name)`, `f.Renamef(...)`      | `f.Rename(ctx, name)`, `f.Renamef(ctx, ...)` |
| `f.RemoveRecursive()` / `...Context`    | `f.RemoveRecursive(ctx)`                     |
| `f.RemoveDirContentsRecursive()`        | `f.RemoveDirContentsRecursive(ctx)`          |
| `f.RemoveDirContents(patterns...)`      | `f.RemoveDirContents(ctx, patterns...)`      |
| `fs.TempFileCopy(src)`                  | `fs.TempFileCopy(ctx, src)`                  |
| `uuiddir.Remove(base, id)`              | `uuiddir.Remove(ctx, base, id)`              |
| `uuiddir.RemoveDir(base, dir)`          | `uuiddir.RemoveDir(ctx, base, dir)`          |

Unchanged (no ctx): `Stat`, `Info`, `Exists`, `CheckExists`, `IsDir`,
`CheckIsDir`, `Size`, `Modified`, `Permissions`, `Open*`, `Touch`, `MakeDir`,
`MakeAllDirs`, `Remove`, `User`/`Group`, `*XAttr`,
symbolic links, `Watch`, `IsEmptyDir`, `IsReadable`, `IsWritable`,
`IsHidden`, all path methods. `Append`, `AppendString`, `ReadJSON`, `WriteJSON`,
`ReadXML`, `WriteXML`, `ReadAllContentHash`, `fs.Move`, `fs.CopyFile`,
`fs.CopyRecursive`, `fs.ReadMemFile`, `fs.Zip`, `fs.UnzipToMemFiles` already
took a context.

`MemFile` implements the same `FileReader` interface, so `MemFile.ReadAll`,
`ReadAllString` and `ContentHash` take a context too.

## Backends

Every backend constructor follows one rule: `New`/`Dial` return the file
system and an error without registering it, `NewAndRegister`/`DialAndRegister`
register and return only an error, and the configuration is a per-instance
`*Options` value (nil for defaults). The tables in
[Upgrading from v1.0.0-beta.1](#upgrading-from-v100-beta1) list the exact
signatures. Changes compared to v0.1:

- `sftpfs`: `Dial(ctx, address, &sftpfs.Options{...})`. Host keys are verified
  by `Options.HostKeyCallback`, a nil callback fails the dial instead of
  accepting any key. URIs with credentials are no longer dialed on first
  use; call `EnsureRegistered(ctx, "sftp://user:password@host", opts)` once.
- `ftpfs`: `Dial(ctx, address, &ftpfs.Options{...})` with the credentials in
  the options. FTPS verifies certificates unless
  `Options.InsecureSkipVerify` is set. `ftps://host` connects with explicit
  TLS on port 21; use `ftps://host:990` for implicit TLS.
- `dropboxfs`: `New(ctx, token, opts)` fetches the account; the prefix is
  `dropbox://<account id without "dbid:">` instead of a random string, so
  URIs can be persisted. `ID()` returns the account id with `dbid:`.
- `s3fs`, `azureblobfs`: `New(...)` with `Options.ReadOnly`; s3fs objects
  written by v0 have keys with a leading `/`, see "Migrating from v0" in
  `s3fs/README.md`.
- `zipfs`: the mode-switching `ZipFileSystem` is split into `zipfs.Reader`
  and `zipfs.Writer` (`NewReader(file, id)`, `NewWriter(file, id)`). A
  `Reader` has no write methods and a `Writer` no read methods, so the wrong
  direction reports `fs.ErrReadOnlyFileSystem` / `fs.ErrWriteOnlyFileSystem`
  instead of "does not exist".
- `multipartfs`: `NewFromRequest(r, maxMemory, id)` replaces
  `FromRequestForm`, `New(form, id)` wraps a `*multipart.Form` the caller
  parsed; both need `fs.Register`. `EscapePath` is removed (it was never
  applied consistently); files uploaded under an already used name get a
  unique name (`a.txt`, `a (2).txt`, ...) instead of shadowing each other,
  and names that are not path elements (`.`, `..`) become `unnamed`. The
  directory name of a form field is escaped (`%` as `%25`, `/` as `%2F`, `.`
  and `..` as `%2E` and `%2E%2E`), file names are used as they are. Every
  file method returns `fs.ErrFileSystemClosed` after `Close`.

## Removed

- `File.ListDirChan` / `File.ListDirRecursiveChan`: use `ListDirIter` or
  `ListDir` with a callback.
- `File.GobEncode` / `File.GobDecode`: encoding a `File` with `encoding/gob`
  used to read the whole file. `MemFile` still round-trips through gob;
  use `fs.ReadMemFile(ctx, file)` to get one.
- `fs.MemDir`: a `MemFile` with a `FileName` ending in `/` is a directory.
- `fs.FileInfoCache`: it was only used by dropboxfs and is internal there now.
- `fs.ReadOnlyBase`, `fs.FullyFeaturedFileSystem`, `fs.RelPathFileSystem`,
  `fs.RunFileSystemTests`, the `tests` package: implementer-facing, see
  [reference-filesystem-interfaces.md](reference-filesystem-interfaces.md).

## Behaviour changes

- `File.Touch` on a file system without native touch support creates a
  missing file and returns `ErrUnsupported` for an existing one instead of
  truncating it.
- `File.RemoveRecursive` does not fail for a missing path.
- `File.IsWritable` is also true for an existing writable directory.
- `File.MoveTo` with an existing destination directory moves into it (as
  before); backends now receive the final path. `Move`, `Rename` and
  `CopyFile` onto an existing directory at the final path fail instead of
  merging into it.
- `File.IsSymbolicLink` is `false` on file systems without symbolic link
  support instead of calling the backend.
- A `File` with a scheme is percent-decoded once when parsed; a scheme-less local
  path is used verbatim, so a local file literally named `a%20b` is reachable.
  The URIs the package builds escape every `%` of the path as `%25`, so
  `dir.Join("a%20b")` addresses a file named `a%20b` on every file system.
  Code that builds a URI by concatenation (`fs.File(prefix + path)`) has to
  escape the path with `fsimpl.EscapeURIPath`, or use `File.Join`.
- File URIs follow RFC 8089. `File.URI()` (was `File.URL()`) of a Windows path is
  `file:///C:/dir/file` instead of `file://C:/dir/file`, and of a UNC path
  `file://server/share/file`; the old Windows form is still parsed, and so is
  `file://localhost/…`. A file URI with another host than `localhost`
  resolves to the `Invalid` file system on platforms other than Windows
  instead of a path relative to the working directory. That includes
  relative URIs that v0 wrote or accepted for relative paths:
  `file://data/uploads` names the host `data`, on Windows the UNC path
  `\\data\uploads` (which connects to that host), and `file://./x`,
  `file://../x` and `file://?/x` name no host and resolve to the `Invalid`
  file system on every platform (on Windows they would be the device paths
  `\\.\x` and `\\?\x`). Convert stored relative file URIs to paths, or to
  absolute file URIs, before upgrading. `file://` and `file://localhost`
  without a path name no file (the empty path, `fs.ErrEmptyPath`), not the
  working directory; `File("file://").Join("x")` is the relative path `x`. Windows long
  paths keep their URI `file:////?/C:/x`; `file:///??/…` names no file.
- Moves and `CopyRecursive` fail with `os.ErrExist` where two source names
  collide at the destination (case or Unicode normalization) instead of
  silently keeping one. Moves always detect it; `CopyRecursive` does in new
  directories and in existing local ones. They also fail on an existing named
  pipe, device or socket at the destination (OneDrive placeholders, which
  Windows reports as irregular files, are replaced). To the local file
  system, they fail with `os.ErrInvalid` on names like `NUL`, `COM1`, `a:b`,
  `name.`, `report?.pdf` (Windows) or names with a NUL byte, before writing
  anything, also for empty directories. `CopyRecursive` returns an error wrapping
  `path.ErrBadPattern` for a malformed pattern before writing anything, also
  for a file source.
- Copies and moves through a `SubFileSystem` or the layers of an
  `OverlayFileSystem` are checked for the same file and for a directory moved
  into itself on every file system, by path: onto itself is a no-op, into
  itself an error wrapping `os.ErrInvalid`, and so is moving an entry of an
  overlay's base layer to its path in the upper layer.
- `sftpfs`, `ftpfs` and `webdavfs` reject paths with a backslash
  (`os.ErrInvalid`), because a server on Windows takes it for a separator.
  For a server on Unix that has files with backslashes in their names, set
  `Options.AllowBackslashInNames`. A copy or move from another file system
  with such names fails at the first of them and leaves the files copied
  until then at the destination; the source of a move is kept.
- `WriteJSON`, `WriteXML`, `MemFile.WriteJSON`/`WriteXML` and
  `NewMemFileWriteJSON`/`NewMemFileWriteXML` don't take an indent any more,
  the `…Indent` variants do. `WriteXMLIndent` with an empty indent puts every
  element on its own line, where `WriteXML(ctx, v, "")` wrote compact XML;
  `WriteXML(ctx, v)` still does.
- `fs.MakeTempDir` uses `os.MkdirTemp`; the new `fs.CreateTempFile` creates a
  temporary file atomically, `fs.TempFile` still only returns a path.
- Every file system satisfies `errors.Is(err, os.ErrNotExist)` /
  `os.ErrExist` / `fs.ErrReadOnlyFileSystem` / `fs.ErrWriteOnlyFileSystem` /
  `fs.ErrFileSystemClosed` / `fs.ErrIsDirectory{}` / `fs.ErrDirectoryNotEmpty{}`
  consistently; `fs.ErrEmptyPath` does not wrap `os.ErrNotExist`.

## Mechanical migration

Most call sites can be rewritten with `gofmt -r` or the `perl -pi` commands
below (they work with the BSD and GNU tools alike). The rewrites below
assume a `ctx` variable is in scope; in tests use `t.Context()` (but
`context.Background()` inside `t.Cleanup`, where the test context is already
cancelled):

```sh
# renamed *Context twins
for m in ReadAll ReadAllString ContentHash WriteAll WriteAllString \
         ListDir ListDirIter ListDirInfo ListDirRecursive ListDirRecursiveIter \
         ListDirInfoRecursive ListDirMax ListDirRecursiveMax RemoveRecursive \
         RemoveDirContentsRecursive RemoveDirContents; do
  find . -name '*.go' -exec perl -pi -e "s/\\.${m}Context\\(/.${m}(/g" {} +
done

# context-less variants that gained a ctx parameter
find . -name '*.go' -exec perl -pi \
  -e 's/\.ReadAll\(\)/.ReadAll(ctx)/g;' \
  -e 's/\.ReadAllString\(\)/.ReadAllString(ctx)/g;' \
  -e 's/\.ContentHash\(\)/.ContentHash(ctx)/g;' \
  -e 's/\.RemoveRecursive\(\)/.RemoveRecursive(ctx)/g;' \
  -e 's/\.RemoveDirContentsRecursive\(\)/.RemoveDirContentsRecursive(ctx)/g;' \
  -e 's/\.ListDir\(func/.ListDir(ctx, func/g;' \
  -e 's/\.ListDirInfo\(func/.ListDirInfo(ctx, func/g;' \
  -e 's/\.ListDirRecursive\(func/.ListDirRecursive(ctx, func/g;' \
  {} +
```

`File.URL()` is `File.URI()` and `fs.FileURLs` is `fs.FileURIs`. The
compiler lists the call sites; don't rewrite every `.URL()` with a regex,
`*http.Request`, `*url.URL` and SDK clients have methods or fields with
that name too.

`WriteAll`, `WriteAllString`, `ListDirMax`, `ListDirIter`, `Glob`, `Truncate`,
`MoveTo`, `Rename`, `RemoveDirContents` and the package functions `ReadHeader`
and `ReadHeaderString` take other arguments before, so their
call sites are easiest to fix from the compiler errors: insert `ctx, ` as the
first argument. Callers of `FileSystem` implementation methods with the same
names (`fileSystem.WriteAll(ctx, path, ...)`) already pass a context and must
not be rewritten.
