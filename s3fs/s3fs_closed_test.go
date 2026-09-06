package s3fs_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

// TestClosedFileSystem verifies that after Close every S3 method returns
// fs.ErrFileSystemClosed instead of dereferencing the now-nil client and
// panicking. No network or credentials are needed: the closed check
// short-circuits before any API call.
func TestClosedFileSystem(t *testing.T) {
	client := s3.New(s3.Options{Region: "us-east-1"})
	s3fsys := s3fs.NewAndRegister(client, "s3fs-closed-test-bucket", false)
	writeFS := s3fsys.(fs.WriteFileSystem)

	require.True(t, fs.IsRegistered(s3fsys), "filesystem should be registered before Close")
	require.NoError(t, s3fsys.Close())
	assert.False(t, fs.IsRegistered(s3fsys), "Close must unregister the filesystem")

	// Close is idempotent.
	assert.NoError(t, s3fsys.Close())

	ctx := t.Context()

	exists, err := s3fsys.(fs.ExistsFileSystem).Exists("/file")
	assert.False(t, exists, "Exists must be false on a closed filesystem")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = writeFS.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.(fs.ReadWriterFileSystem).OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = writeFS.MakeDir("/dir", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = writeFS.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.(fs.ReadAllFileSystem).ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.WriteAllFileSystem).WriteAll(ctx, "/file", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.TouchFileSystem).Touch("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
