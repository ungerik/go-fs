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
// fs.ErrFileSystemClosed (or false for Exists) instead of dereferencing the
// now-nil client and panicking. No network or credentials are needed: the
// closed check short-circuits before any API call.
func TestClosedFileSystem(t *testing.T) {
	client := s3.New(s3.Options{Region: "us-east-1"})
	s3fsys := s3fs.NewAndRegister(client, "s3fs-closed-test-bucket", false)

	require.True(t, fs.IsRegistered(s3fsys), "filesystem should be registered before Close")
	require.NoError(t, s3fsys.Close())
	assert.False(t, fs.IsRegistered(s3fsys), "Close must unregister the filesystem")

	// Close is idempotent.
	assert.NoError(t, s3fsys.Close())

	ctx := t.Context()

	assert.False(t, s3fsys.(fs.ExistsFileSystem).Exists("/file"),
		"Exists must be false on a closed filesystem")

	_, err := s3fsys.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.OpenWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.OpenReadWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.MakeDir("/dir", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.ListDirInfo(ctx, "/dir", func(*fs.FileInfo) error { return nil }, nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = s3fsys.(fs.ReadAllFileSystem).ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.WriteAllFileSystem).WriteAll(ctx, "/file", []byte("x"), nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "/a", "/b", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.TouchFileSystem).Touch("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
