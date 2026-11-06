# Git File System

A read-only [go-fs](https://github.com/ungerik/go-fs) file system for one
commit of a remote git repository: GitHub, GitLab, Gitea, Bitbucket or any
other server that speaks git smart HTTP protocol version 2, like
`git http-backend`.

The protocol client, the packfile reader and the object parsing are
implemented with the standard library only: no git binary, no go-git, no
clone on disk.

## Installation

Get the backend together with the go-fs core module, at the same version:

```bash
go get github.com/ungerik/go-fs@v1.0.0-rc.8 github.com/ungerik/go-fs/gitfs@v1.0.0-rc.8
```

## Usage

```go
import (
    "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/gitfs"
)

func readVersion(ctx context.Context) (string, error) {
    repo, err := gitfs.New(ctx, "https://github.com/ungerik/go-fs.git", "v1.0.0-rc.6", nil)
    if err != nil {
        return "", err
    }
    defer repo.Close()
    err = fs.Register(repo)
    if err != nil {
        return "", err
    }
    // repo.Prefix() is "git+https://github.com/ungerik/go-fs.git@v1.0.0-rc.6"
    return repo.RootDir().Join("VERSION").ReadAllString(ctx)
}
```

The ref is a branch, a tag, a full ref name like `refs/heads/main`, or a full
hex commit ID. An empty ref is the default branch (the `HEAD` of the
repository). A branch wins over a tag of the same name, and annotated tags are
peeled to their commit. The commit is resolved once by `New`: the file system
keeps showing it when the branch moves on, and `Commit()` returns its ID.

### Authentication

```go
// A GitHub or GitLab access token, also raises the GitHub API rate limit
repo, err := gitfs.New(ctx, url, "main", &gitfs.Options{Token: os.Getenv("GITHUB_TOKEN")})

// HTTP Basic authentication, or credentials in the URL
repo, err := gitfs.New(ctx, url, "main", &gitfs.Options{Username: "user", Password: "secret"})
repo, err := gitfs.New(ctx, "https://user:secret@git.example.com/team/repo.git", "main", nil)
```

Credentials are never part of the prefix, the ID or error messages.
`Options.HTTPClient` sets the client for timeouts and proxies.

## What it costs

Nothing is downloaded up front, and everything is fetched once:

| Operation | Requests |
| --- | --- |
| `New` with a branch or tag | 2: the capabilities of the server and `ls-refs` |
| `New` with a commit ID | 2: the capabilities and the commit |
| The first `Stat`, listing or `OpenReader` | 1 for the commit, without its trees |
| A directory that was not loaded yet | 1 for its tree (`filter tree:0`), so a path costs one request per directory along it |
| `ListDirRecursive` | 1 for all the trees below the directory (`filter blob:none`) |
| The content of a file | 1, then it is kept in a cache of `Options.CacheSize` (64 MiB) |
| Sizes of the files of a directory, for `Stat` and the listings | 1 for the content of all of them, git can't return the size of a file without its content; a response above `Options.MaxFetchSize` (1 GiB) is split into more requests |

On github.com the GitHub shortcuts make the typical cases cheaper:

- The trees come from the GitHub REST API, which returns the sizes of the
  files, so listing a directory doesn't fetch the content of its files.
  Without `Options.Token` the API allows 60 requests per hour.
- The content of a file comes from raw.githubusercontent.com, checked
  against the object ID of the file in its tree. Reading one file in the
  root directory of the latest version costs five requests including `New`.
- When the API or raw.githubusercontent.com fails, for example over the rate
  limit or for a private repository without `Options.Token`, gitfs uses the
  git protocol instead for the rest of the lifetime of the file system.
- JSON can't hold names that are not valid UTF-8, so a directory with a name
  with a U+FFFD replacement character is read through the git protocol, and
  lists the same entries as without the shortcuts. So is a tree with more
  entries than one response of the API can hold, for `ListDirRecursive` of
  a large repository; the API stays in use for the other trees.

`Options.DisableGitHub` turns the shortcuts off. Servers that don't support
object filters send the whole commit with all its files on the first access.

## How git concepts map to the file system

- **Files and directories** are the blobs and trees of the commit.
  Executable files have the permissions `0755`, other files `0644` and
  directories `0755`.
- **Modification time:** git stores no times of files, so every entry has
  the committer time of the commit.
- **Not exposed:** symbolic links and submodules, and entries whose names
  can't be the names of files of a checkout: names with a slash or backslash,
  names that are not valid UTF-8, `.` and `..`, and `.git` in any case.
- **Sys:** the `FileInfo.Sys` of an entry is the hex object ID of its blob or
  tree as string.
- **Errors:** a missing ref is `gitfs.ErrRefNotFound`, a server without
  protocol version 2 is `gitfs.ErrProtocolV2Required`, and HTTP status 404 is
  an error wrapping `os.ErrNotExist`, 401 and 403 wrap `os.ErrPermission`.
  Every write returns `fs.ErrReadOnlyFileSystem`. After `Close` every method
  returns `fs.ErrFileSystemClosed`.
- **Limits:** a file larger than `Options.MaxObjectSize` (1 GiB) can't be
  read, every packfile object and delta is checked against it before it is
  decompressed. All objects of one response together can't be larger than
  `Options.MaxFetchSize` (1 GiB), which bounds the memory a server can make
  gitfs use. The objects are verified against their IDs and the checksum
  of each packfile.
- **Prefix:** `git+` followed by the repository URL, `@` and the ref, or
  `HEAD` for an empty ref. The ref is escaped like a URL path segment, so
  the branch `feature/x` has the prefix `git+<repository URL>@feature%2Fx`,
  which can't be confused with the directory `x` of the branch `feature`. A
  second file system for the same repository and ref can't be registered
  while the first one is.

## Testing

```bash
go test ./...
```

The tests run offline against:

- a fake git server in Go (`internal/gittest`) that also fakes the GitHub
  REST API and raw.githubusercontent.com, and counts the requests,
- `git http-backend` of the local git installation, skipped without git,
- nginx with `git http-backend` and Basic authentication in a Docker
  container (`Dockerfile.git-test`), skipped without Docker.

The go-fs conformance suite runs against all three. The tests against
github.com read this repository at release tags, whose content never
changes:

```bash
GOFS_ONLINE_TESTS=1 go test -run Online ./...
```

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
