# gofs

`ls`, `cp`, `mv`, `rm` and `cat` for S3, Azure Blob Storage, Dropbox, SFTP,
FTP, SMB, WebDAV, HTTP and local files, with one command copying between
any two of them:

```bash
gofs cp -r s3://my-bucket/reports sftp://alice@example.com/backup
```

There is no config file: `gofs` logs in with environment variables and the
keys and profiles that `ssh`, the AWS CLI and `az login` already set up.

## Installation

Download the archive for your platform from the
[releases](https://github.com/ungerik/go-fs/releases), from v1.0.0-rc.7 on,
and put `gofs` (`gofs.exe` on Windows) on your `PATH`. Each archive has
a `.sha256` file with its checksum.

Or build it with Go 1.26 or newer:

```bash
go install github.com/ungerik/go-fs/cmd/gofs@latest
```

A release of `gofs` is built with the go-fs modules of the same release,
which its `go.mod` requires; `gofs version` prints both versions. The
release archives are the same build as `go install`. To build `gofs` with the
code of a clone, run `go install ./cmd/gofs` in it.

## What it does

- **Copy and move between file systems** — `cp -r` and `mv` take any two of
  them; `mv` renames on the server where it can, else copies and deletes.
- **Pipe** — `cat` writes files to stdout and `put` writes stdin to a file,
  so `pg_dump mydb | gofs put smb://backup@nas/dumps/mydb.sql` needs no local
  temp file.
- **Check a transfer** — `cmp -r` compares two directory trees on different
  file systems and exits with 1 if they differ; `hash` prints SHA-256, MD5
  or the Dropbox content hash.
- **Log in like `ssh`** — SFTP uses the SSH agent, the keys of `~/.ssh` and
  `known_hosts`; S3 reads `~/.aws` like the AWS CLI; Azure takes `az login`.
- **Keep passwords out of the shell history** — credentials come from
  environment variables, and `gofs env` shows which of them are set, never
  their values.

`gofs` is built on [go-fs](https://github.com/ungerik/go-fs), the Go library
for these file systems.

## Commands

| Command | Arguments | Does |
| ------- | --------- | ---- |
| `ls` | `[-l] [-r] [-a] [-u] [URI...]` | Lists directories, or prints the names of files. `-l` adds permissions, size and modification time, `-r` lists the files of all sub-directories, `-a` includes hidden files, `-u` prints URIs |
| `stat` | `URI...` | Prints the URI, name, type, size, modification time and mode |
| `cat` | `URI...` | Writes the content of files to stdout |
| `put` | `URI` | Writes stdin to a file, creating missing parent directories. If stdin fails or the command is interrupted, an existing file keeps its content and no partial new file is left, see below |
| `cp` | `[-r] SOURCE... DEST` | Copies files, with `-r` directories. A directory goes into an existing directory `DEST`, else it becomes `DEST`. The content of `.` and of the root of a file system, like `s3://bucket`, goes into an existing `DEST` itself |
| `mv` | `SOURCE... DEST` | Moves or renames files and directories, on the server when both are on one that can rename, else by copy and delete |
| `rm` | `[-r] [-f] URI...` | Removes files and empty directories, with `-r` directories with their content. `-f` ignores missing files. Refuses paths that end with `.` or `..` |
| `mkdir` | `[-p] URI...` | Creates directories, with `-p` also the missing parents |
| `touch` | `URI...` | Creates empty files or updates their modification time |
| `hash` | `[-a dropbox\|sha256\|md5] URI...` | Prints content hashes, by default the Dropbox `content_hash` |
| `cmp` | `[-r] URI URI` | Compares two files or directories, exits with 1 if they differ |
| `env` | | Lists the environment variables below and which of them are set, never their values |
| `version` | | Prints the version |
| `help` | `[COMMAND]` | Prints the usage, with a command its flags like `gofs COMMAND -h` |

Several sources need an existing destination directory, and so does a
destination with a trailing slash. Short boolean flags can be combined, like
`rm -rf`. Flags go before the other arguments.

`put` writes stdin directly to S3, Azure Blob Storage, Dropbox, WebDAV and
FTP, which discard an incomplete upload; WebDAV and FTP buffer it in memory
until stdin ends. On the local file system, SFTP and SMB it replaces an
existing file like an editor saving it: it writes a hidden `.gofs-put-*` file
next to it with its permissions and renames that over the file once stdin has
ended. So a symbolic link is replaced with a file instead of written through,
other hard links keep the old content, and the owner and group are those of
the new file. A killed process can leave the hidden file behind. A read-only
file is refused, a named pipe or device is written directly, and so is a file
in a directory where no hidden file can be created, which then loses its
content if stdin fails.

Exit codes: 0 for success, 1 for errors and for `cmp` of differing files,
2 for invalid arguments.

## URIs

Arguments are local paths or URIs:

| URI | File system |
| --- | ----------- |
| `/local/path`, `file:///local/path` | Local |
| `http://host/path`, `https://host/path` | HTTP, read only |
| `ftp://[user@]host[:port]/path`, `ftps://...` | FTP and FTPS |
| `sftp://[user@]host[:port]/path` | SFTP |
| `smb://[user@]host[:port]/share/path` | SMB share |
| `webdav:///path` | WebDAV server of `GOFS_WEBDAV_URL` |
| `s3://bucket/key` | S3 bucket |
| `azblob://ACCOUNT.blob.core.windows.net/CONTAINER/path` | Azure Blob Storage container |
| `azblob:///CONTAINER/path` | Container of the account of the environment |
| `dropbox:///path` | Dropbox account of the token |

A file system connects on first use and closes when the command ends.
The URIs that `ls -u` prints, like `webdav://alice@cloud.example.com/dav/x`
or `dropbox://AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc/x`, work as arguments
too.

Put passwords in the environment instead of in URIs, which end up in the
shell history. A password in a URI must percent-encode `/`, `?`, `#` and `@`.
Paths may percent-encode characters like a space as `%20`, so a `%` must be
written as `%25`.

## Environment

| Variable | Scheme | Use |
| -------- | ------ | --- |
| `GOFS_FTP_USER` | `ftp`, `ftps` | Username if the URI has none, else anonymous login |
| `GOFS_FTP_PASSWORD` | `ftp`, `ftps` | Password if the URI has none, only for a username of the URI or `GOFS_FTP_USER` other than `anonymous` or `ftp`, never for an anonymous login |
| `GOFS_FTP_INSECURE_SKIP_VERIFY` | `ftps` | `true`: don't verify the server certificate |
| `GOFS_SFTP_USER` | `sftp` | Username if the URI has none, else the local username |
| `GOFS_SFTP_PASSWORD` | `sftp` | Password if the URI has none |
| `GOFS_SFTP_KEY_FILE` | `sftp` | Private key file instead of `~/.ssh/id_ed25519`, `id_ecdsa` and `id_rsa` |
| `GOFS_SFTP_KEY_PASSPHRASE` | `sftp` | Passphrase of the private key files |
| `SSH_AUTH_SOCK` | `sftp` | Socket of the SSH agent, whose keys are tried first |
| `GOFS_SFTP_KNOWN_HOSTS` | `sftp` | Known hosts file instead of `~/.ssh/known_hosts` |
| `GOFS_SFTP_INSECURE_IGNORE_HOST_KEY` | `sftp` | `true`: don't verify the host key |
| `GOFS_SMB_USER` | `smb` | Username if the URI has none, else a guest session |
| `GOFS_SMB_PASSWORD` | `smb` | Password if the URI has none, only for a username of the URI or `GOFS_SMB_USER` other than `Guest`, never for a guest session |
| `GOFS_SMB_DOMAIN` | `smb` | NTLM domain |
| `GOFS_WEBDAV_URL` | `webdav` | Required: the `http` or `https` URL of the server; `webdav:///path` is below it |
| `GOFS_WEBDAV_USER` | `webdav` | Username, overrides the one of `GOFS_WEBDAV_URL` |
| `GOFS_WEBDAV_PASSWORD` | `webdav` | Password for basic authentication |
| `GOFS_WEBDAV_TOKEN` | `webdav` | Bearer token, sent instead of basic authentication |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_PROFILE` | `s3` | Credentials, read by the AWS SDK like by the AWS CLI, also from `~/.aws` |
| `AWS_REGION` | `s3` | Region of the buckets |
| `AWS_ENDPOINT_URL_S3` | `s3` | Endpoint of other S3 services, like MinIO or Cloudflare R2 |
| `AWS_REQUEST_CHECKSUM_CALCULATION`, `AWS_RESPONSE_CHECKSUM_VALIDATION` | `s3` | `when_required` for S3 services without CRC checksums |
| `GOFS_S3_PATH_STYLE` | `s3` | `true`: path-style requests, needed by MinIO and bucket names with dots |
| `AZURE_STORAGE_CONNECTION_STRING` | `azblob` | Connection string, used before the other credentials |
| `AZURE_STORAGE_ACCOUNT` | `azblob` | Storage account of `azblob:///CONTAINER/path` |
| `AZURE_STORAGE_SAS_TOKEN` | `azblob` | SAS token, used before the account key |
| `AZURE_STORAGE_KEY` | `azblob` | Account key |
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_CLIENT_SECRET` | `azblob` | Without the variables above: the credentials of [azidentity](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azidentity#DefaultAzureCredential), also managed identities and `az login` |
| `DROPBOX_ACCESS_TOKEN` | `dropbox` | Access token, valid for 4 hours |
| `DROPBOX_REFRESH_TOKEN` | `dropbox` | Refresh token, used instead of the access token |
| `DROPBOX_APP_KEY` | `dropbox` | App key that the refresh token was issued to |
| `DROPBOX_APP_SECRET` | `dropbox` | App secret, not needed for tokens issued with PKCE |

SFTP verifies host keys like `ssh` does: add a server with
`ssh-keyscan host >> ~/.ssh/known_hosts` or by connecting once with `ssh`.
`gofs` offers the server the algorithms of the keys in `known_hosts` for
the host, so it works when the file has only some of the keys of the server.

## Examples

```bash
# Upload a directory to a MinIO bucket
export AWS_ENDPOINT_URL_S3=http://localhost:9000 AWS_REGION=us-east-1 GOFS_S3_PATH_STYLE=true
gofs cp -r ./site s3://www/

# Compare a Nextcloud folder with a local copy
export GOFS_WEBDAV_URL=https://cloud.example.com/remote.php/dav/files/alice
export GOFS_WEBDAV_USER=alice GOFS_WEBDAV_PASSWORD=app-password
gofs cmp -r webdav:///Photos ~/Photos

# Stream a database dump to an SMB share
pg_dump mydb | GOFS_SMB_PASSWORD=secret gofs put smb://backup@nas/dumps/mydb.sql
```
