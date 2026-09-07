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
func TestClosedFileSystem(t *testing.T) {
	f := &fileSystem{
		PathHelper: pathHelper("ftp://user@example.com"),
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

	_, err = f.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = f.OpenAppendWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = f.RemoveAll(ctx, "/dir")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
