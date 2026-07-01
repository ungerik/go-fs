package sftpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestClosedFileSystem verifies that a closed SFTP file system returns
// fs.ErrFileSystemClosed from every operation instead of silently dialing a
// new connection. Connection parameters are deliberately set so that, without
// the closed guard, getClient would attempt to reconnect — the test proves the
// closed state takes precedence over the auto-reconnect logic.
func TestClosedFileSystem(t *testing.T) {
	f := &fileSystem{
		prefix:              "sftp://user@example.com",
		closed:              true,
		address:             "sftp://user@example.com",
		credentialsCallback: UsernameAndPassword("user", "password"),
		hostKeyCallback:     AcceptAnyHostKey,
	}

	// Close on an already-closed file system is a safe no-op.
	require.NoError(t, f.Close())

	_, err := f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.MakeDir("/dir", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenReadWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenAppendWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Truncate("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Move("/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.ListDirInfo(t.Context(), "/dir", func(*fs.FileInfo) error { return nil }, nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
