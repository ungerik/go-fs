package ftpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestClosedFileSystem verifies that a closed FTP file system returns
// fs.ErrFileSystemClosed from every operation instead of silently dialing a
// new connection from the URL credentials.
//
// Methods that contact the server eagerly fail immediately. OpenWriter and
// OpenAppendWriter buffer in memory and only contact the server when the
// returned writer is closed, so for those the error surfaces from Close.
func TestClosedFileSystem(t *testing.T) {
	f := &fileSystem{
		PathHelper: pathHelper("ftp://user@example.com", false),
		closed:     true,
	}
	ctx := t.Context()

	// Close on an already-closed file system is a safe no-op.
	require.NoError(t, f.Close())

	_, err := f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.MakeDir("/dir", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Move("/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.WriteAll(ctx, "/file", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.Append(ctx, "/file", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	// OpenWriter / OpenAppendWriter buffer in memory: opening succeeds, the
	// closed error is reported when the buffered writer is flushed on Close.
	w, err := f.OpenWriter("/file", 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)

	aw, err := f.OpenAppendWriter("/file", 0)
	require.NoError(t, err)
	_, err = aw.Write([]byte("x"))
	require.NoError(t, err)
	assert.ErrorIs(t, aw.Close(), fs.ErrFileSystemClosed)
}
