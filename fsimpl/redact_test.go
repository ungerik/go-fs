package fsimpl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedactURI checks the password redaction of URIs, also of URIs that
// url.Parse rejects or misreads because a password contains an unencoded
// '/', '?' or '#'. Most cases come from the tests of the redaction functions
// that RedactURI replaced in the fs, httpfs, s3fs and sftpfs packages.
func TestRedactURI(t *testing.T) {
	for uri, want := range map[string]string{
		// Parsed by url.Parse
		"ftp://user:hunter2@host/dir/file.txt": "ftp://user:xxxxx@host/dir/file.txt",
		"ftp://user@host/dir/file.txt":         "ftp://user@host/dir/file.txt",
		"ftp://host/dir/a@b.txt":               "ftp://host/dir/a@b.txt",
		"sftp://:secret@host":                  "sftp://:xxxxx@host",
		"sftp://user:@host":                    "sftp://user:xxxxx@host",
		"http://host/key?mail=a@b":             "http://host/key?mail=a@b",
		"http://[::1]:8080/a:b@c":              "http://[::1]:8080/a:b@c",
		"file:///home/a:b@c.txt":               "file:///home/a:b@c.txt",
		// Without scheme there is no user info
		"/local/a:b@c.txt":  "/local/a:b@c.txt",
		"/local/a:b@c%.txt": "/local/a:b@c%.txt",
		"user:secret@host":  "user:secret@host",

		// Rejected by url.Parse
		"ftp://user:hunter2@host/dir/100%.txt": "ftp://user:xxxxx@host/dir/100%.txt",
		"ftp://user:hunter2@host:port/dir":     "ftp://user:xxxxx@host:port/dir",
		"ftp://user:hunter2@host/dir/\x7f.txt": "ftp://user:xxxxx@host/dir/\x7f.txt",
		"ftp://user:hunter2@[::1/dir":          "ftp://user:xxxxx@[::1/dir",
		"sftp://user:secret@ho st/path":        "sftp://user:xxxxx@ho st/path",
		"ftp://user:hunter2@x@host/100%.txt":   "ftp://user:xxxxx@host/100%.txt",
		"ftp://user@host/dir/100%.txt":         "ftp://user@host/dir/100%.txt",
		"ftp://host/dir/100%.txt":              "ftp://host/dir/100%.txt",
		"ftp://host/a@b/100%.txt":              "ftp://host/a@b/100%.txt",
		"http://host/key%zz?mail=a@b":          "http://host/key%zz?mail=a@b",
		// "host:22/a%zz" is no valid user info even with the '/' encoded,
		// so the '@' is part of the path. The port and path were replaced
		// by "xxxxx" as if they were a password: "sftp://host:xxxxx@b"
		"sftp://host:22/a%zz@b": "sftp://host:22/a%zz@b",

		// A password with an unencoded '/', '?' or '#' that url.Parse
		// rejects as port, it ends with the last '@'
		"ftp://user:hunter2/x@host/dir/100%.txt": "ftp://user:xxxxx@host/dir/100%.txt",
		"ftp://user:hunter2?x@host/dir/100%.txt": "ftp://user:xxxxx@host/dir/100%.txt",
		"ftp://user:hunter2#x@host/dir/100%.txt": "ftp://user:xxxxx@host/dir/100%.txt",
		"http://user:top?secret@host/key":        "http://user:xxxxx@host/key",
		"http://user:top/secret@host/key%zz":     "http://user:xxxxx@host/key%zz",
		"sftp://bob:ab/cd@host":                  "sftp://bob:xxxxx@host",
		"sftp://bob:ab#cd@host":                  "sftp://bob:xxxxx@host",
		"sftp://bob:ab/cd@host:22/path":          "sftp://bob:xxxxx@host:22/path",
		"ftp://alice@example.com:se/cret@host/x": "ftp://alice@example.com:xxxxx@host/x",

		// A password of digits or an empty one with an unencoded '/', '?'
		// or '#', which url.Parse takes for a port, was not redacted.
		// A host with a port and an '@' in the path looks the same.
		"sftp://user:/secret@host/x":             "sftp://user:xxxxx@host/x",
		"ftp://u:123?x@host/":                    "ftp://u:xxxxx@host/",
		"sftp://bob:12/34@127.0.0.1":             "sftp://bob:xxxxx@127.0.0.1",
		"sftp://bob:12#34@host":                  "sftp://bob:xxxxx@host",
		"ftp://u:123/x@host/dir/100%.txt":        "ftp://u:xxxxx@host/dir/100%.txt",
		"http://localhost:8080/me@example.com":   "http://localhost:xxxxx@example.com",
		"http://localhost:8080/me%40example.com": "http://localhost:8080/me%40example.com",

		// A password with an unencoded '@' and '/' of a URI that url.Parse rejects
		"s3://user:se@cr/et@minio.example.com/b%zz": "s3://user:xxxxx@minio.example.com/b%zz",
		// but not if the user info would be invalid up to the last '@'
		"http://user:topsecret@host/key%zz?m=a@b": "http://user:xxxxx@host/key%zz?m=a@b",
		// A URI that url.Parse accepts is redacted as parsed
		"sftp://bob:a@b/c@host":                  "sftp://bob:xxxxx@b/c@host",
		"sftp://user:secret@host/me@example.com": "sftp://user:xxxxx@host/me@example.com",
	} {
		assert.Equal(t, want, RedactURI(uri), "RedactURI(%q)", uri)
	}
}

// TestRedactAddress checks the redaction of addresses that are rejected
// because of an '@' after the host or that url.Parse rejects. The cases
// come from the tests of the redaction functions that RedactAddress
// replaced in the sftpfs, ftpfs and smbfs packages. Those of ftpfs and
// smbfs kept an '@' after the host, like in "u:S3cret@host/a@b" and
// "ftp://host/a:b@c", which these packages now reject.
func TestRedactAddress(t *testing.T) {
	for address, want := range map[string]string{
		"sftp://user:secret@host":       "sftp://user:xxxxx@host",
		"sftp://user@host":              "sftp://user@host",
		"sftp://user@host/path":         "sftp://user@host/path",
		"sftp://user:secret@ho st/path": "sftp://user:xxxxx@ho st/path",
		"sftp://user:secret@[::1":       "sftp://user:xxxxx@[::1",
		"sftp://ho st":                  "sftp://ho st",
		"sftp://:secret@host":           "sftp://:xxxxx@host",
		"host":                          "host",
		// Without scheme
		"user:secret@host":    "user:xxxxx@host",
		"u:S3cret@host/share": "u:xxxxx@host/share",
		// A password with an unencoded '/', '?', '#' or '@'
		"sftp://bob:ab/cd@host":         "sftp://bob:xxxxx@host",
		"sftp://bob:12/34@127.0.0.1":    "sftp://bob:xxxxx@127.0.0.1",
		"sftp://bob:12?34@host":         "sftp://bob:xxxxx@host",
		"sftp://bob:ab#cd@host":         "sftp://bob:xxxxx@host",
		"sftp://bob:a@b/c@host":         "sftp://bob:xxxxx@host",
		"sftp://bob:ab/cd@host:22/path": "sftp://bob:xxxxx@host:22/path",
		"u:S3cret@host/a@b":             "u:xxxxx@b",
		// A username with a '/', '?' or '#' can't be told apart from the host and path
		"sftp://ali/ce:secret@host":        "sftp://xxxxx@host",
		"sftp://host/dir/name@example.com": "sftp://xxxxx@example.com",
		"ftp://host/a:b@c":                 "ftp://xxxxx@c",
	} {
		assert.Equal(t, want, RedactAddress(address), "RedactAddress(%q)", address)
	}
}
