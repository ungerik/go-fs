package sftpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestClosedFileSystem verifies that a closed SFTP file system returns
// fs.ErrFileSystemClosed from every operation instead of silently dialing a
// new connection. The file system was never dialed and has all settings
// to reconnect, so without the closed guard getClient would dial
// example.com: the closed state takes precedence over reconnecting.
func TestClosedFileSystem(t *testing.T) {
	f, _, err := newFileSystem("sftp://user@example.com", &Options{
		Password:        "password",
		HostKeyCallback: AcceptAnyHostKey,
	})
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Close on an already-closed file system is a safe no-op.
	require.NoError(t, f.Close())

	_, err = f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.Lstat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.MakeDir("/dir", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	r, err := f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.Nil(t, r, "OpenReader must return a nil interface with an error")

	w, err := f.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.Nil(t, w, "OpenWriter must return a nil interface with an error")

	rw, err := f.OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.Nil(t, rw, "OpenReadWriter must return a nil interface with an error")

	aw, err := f.OpenAppendWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.Nil(t, aw, "OpenAppendWriter must return a nil interface with an error")

	err = f.Truncate(t.Context(), "/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Move(t.Context(), "/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	// Moves that need no request to the server
	err = f.Move(t.Context(), "/a", "/a")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "Move onto itself")
	err = f.Move(t.Context(), "/a", "/a/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "Move into itself")

	err = f.RemoveAll(t.Context(), "/dir")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = f.RemoveAll(t.Context(), "/")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "RemoveAll of the root directory")

	err = f.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.ListDir(t.Context(), "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.ListDirRecursive(t.Context(), "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
