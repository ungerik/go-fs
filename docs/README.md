# go-fs documentation

`go-fs` gives you one API — the `fs.File` type — over the local disk, S3, SFTP,
FTP, SMB, WebDAV, Azure Blob, Dropbox, HTTP, ZIP and tar archives, embedded
assets and memory.

New here? Start with [Getting started](tutorial-getting-started.md).

These documents are organised by what you are trying to do, following the
[Diátaxis](https://diataxis.fr) framework. The complete API reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/ungerik/go-fs).

## Tutorials — learning by building

Start here if you are new. Each one takes you from nothing to something
working.

| Document                                           | You will build                                     |
| -------------------------------------------------- | -------------------------------------------------- |
| [Getting started](tutorial-getting-started.md)     | A program that reads and writes files, running unchanged against local disk, memory and HTTP |
| [Implement a file system](tutorial-implement-a-filesystem.md) | A complete backend in ~120 lines that passes the conformance suite |

## How-to guides — solving a specific problem

You know the basics and want to get something done.

| Document                                           | Task                                               |
| -------------------------------------------------- | -------------------------------------------------- |
| [Connect a remote backend](howto-connect-a-remote-backend.md) | Dial S3, SFTP, FTP, SMB, WebDAV, Azure or Dropbox  |
| [Test with MemFileSystem](howto-test-with-memfilesystem.md) | Make code that touches files testable without the disk |
| [Serve files over HTTP](howto-serve-files-over-http.md) | Single files, directories, embedded assets, range requests |
| [Handle form uploads](howto-handle-form-uploads.md) | Treat multipart uploads as regular files           |
| [Work with archives](howto-work-with-archives.md)  | Read and write ZIP and tar, zip in memory          |
| [Copy, move and compare](howto-copy-move-and-compare.md) | Move data between backends, compare contents       |
| [Run the conformance suite](howto-run-the-conformance-suite.md) | Verify a backend you wrote                         |
| [Migrating to v1.0](MIGRATION_v1.md)               | Upgrade from v0.x                                  |

## Reference — the details

Look things up. For the per-symbol API, use
[pkg.go.dev](https://pkg.go.dev/github.com/ungerik/go-fs); these documents
cover what spans several types.

| Document                                           | Contents                                           |
| -------------------------------------------------- | -------------------------------------------------- |
| [FileSystem interfaces](reference-filesystem-interfaces.md) | All 28 interfaces, every fallback, the backend support matrix |
| [Errors](reference-errors.md)                      | Every sentinel and typed error, and how to check it |
| [fsimpl](reference-fsimpl.md)                      | Helpers for implementing a backend                 |
| [fstest](reference-fstest.md)                      | The conformance suite, mocks and the Docker gate   |
| [uuiddir](reference-uuiddir.md)                    | UUID-named directories without unbounded fan-out   |

### Backend and package READMEs

Every module and every sub package that implements a `FileSystem` has a
README of its own with its setup, its concept mapping and how to run its
tests.

| Document                                           | Contents                                           |
| -------------------------------------------------- | -------------------------------------------------- |
| [s3fs](../s3fs/README.md)                          | S3 credentials, S3-compatible services, concept mapping |
| [azureblobfs](../azureblobfs/README.md)            | Azure credentials, blob name mapping, Azurite tests |
| [sftpfs](../sftpfs/README.md)                      | Host keys, reconnects, shared connections          |
| [ftpfs](../ftpfs/README.md)                        | FTPS TLS options, control connection behaviour     |
| [smbfs](../smbfs/README.md)                        | NTLM authentication, share paths, Samba tests      |
| [webdavfs](../webdavfs/README.md)                  | Basic auth and bearer tokens, WebDAV method mapping |
| [dropboxfs](../dropboxfs/README.md)                | Dropbox app setup, access token, manual test run   |
| [httpfs](../httpfs/README.md)                      | Reading HTTP URLs as files, the shared http.Client |
| [zipfs](../zipfs/README.md)                        | Reading and writing ZIP archives                   |
| [tarfs](../tarfs/README.md)                        | Reading and writing tar, .tar.gz and .tgz archives |
| [multipartfs](../multipartfs/README.md)            | Uploaded form files as a file system               |
| [tools](../tools/README.md)                        | The pinned staticcheck and gosec versions          |

## Explanation — why it works this way

Background reading. None of it is needed to use the library, all of it is
useful when something surprises you.

| Document                                           | Question answered                                  |
| -------------------------------------------------- | -------------------------------------------------- |
| [Why File is a string](explanation-the-file-type.md) | Why not an interface or a struct?                  |
| [Paths, URIs and the registry](explanation-path-and-uri-model.md) | How does a string become a file system?            |
| [The context rule](explanation-context-rule.md)    | Why do only some methods take a `ctx`?             |
| [Optional interfaces and emulation](explanation-optional-interfaces.md) | Why 28 interfaces, and why does everything work everywhere? |

## Project documents

| Document                     | Contents                                |
| ---------------------------- | --------------------------------------- |
| [README](../README.md)       | Overview and API tour                   |
| [CHANGELOG](../CHANGELOG.md) | Release history                         |
| [V1 roadmap](V1_ROADMAP.md)  | Plan of record for the v1.0 work        |
| [AGENTS.md](../AGENTS.md)    | Repository conventions for contributors |

## Finding your way

| I want to…                               | Go to                                              |
| ---------------------------------------- | -------------------------------------------------- |
| Read a file from S3                      | [Connect a remote backend](howto-connect-a-remote-backend.md) |
| Stop my tests touching the disk          | [Test with MemFileSystem](howto-test-with-memfilesystem.md) |
| Know why my write returned "read-only"   | [Errors](reference-errors.md)                      |
| Know if a backend supports `Truncate`    | [FileSystem interfaces](reference-filesystem-interfaces.md) |
| Understand why `Size()` takes no context | [The context rule](explanation-context-rule.md)    |
| Add support for a new storage service    | [Implement a file system](tutorial-implement-a-filesystem.md) |
| Upgrade from v0.x                        | [Migrating to v1.0](MIGRATION_v1.md)               |
