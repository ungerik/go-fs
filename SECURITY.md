# Security policy

## Supported versions

Security fixes are released for the latest minor version of v1, for the
root module `github.com/ungerik/go-fs`, for the backend modules
(`azureblobfs`, `dropboxfs`, `ftpfs`, `gitfs`, `onedrivefs`, `s3fs`, `sftpfs`,
`smbfs`, `webdavfs`) and for the `cmd/gofs` command line tool, which are
released in lockstep with it. Older versions get no fixes; upgrade to the latest
release.

## Reporting a vulnerability

Please report vulnerabilities privately through a
[GitHub security advisory](https://github.com/ungerik/go-fs/security/advisories/new),
not in a public issue or pull request.

Include the affected module and version, a description of the problem and,
if possible, a test or program that reproduces it. The advisory is
published once a release with the fix is available.
