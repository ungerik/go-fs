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
	s3fsys, err := s3fs.New(client, "s3fs-closed-test-bucket", nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(s3fsys))
	writeFS := s3fsys.(fs.WriteFileSystem)

	require.True(t, fs.IsRegistered(s3fsys), "filesystem should be registered before Close")
	require.NoError(t, s3fsys.Close())
	assert.False(t, fs.IsRegistered(s3fsys), "Close must unregister the filesystem")

	// Close is idempotent.
	assert.NoError(t, s3fsys.Close())

	ctx := t.Context()

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

	err = s3fsys.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/dir")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = s3fsys.(fs.ListDirRecursiveFileSystem).ListDirRecursive(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// TestReadOnlyFileSystem verifies that the write methods of a file system
// with Options.ReadOnly return fs.ErrReadOnlyFileSystem without a request.
func TestReadOnlyFileSystem(t *testing.T) {
	client := s3.New(s3.Options{Region: "us-east-1"})
	s3fsys, err := s3fs.New(client, "s3fs-read-only-test-bucket", &s3fs.Options{ReadOnly: true})
	require.NoError(t, err)
	ctx := t.Context()

	_, err = s3fsys.(fs.WriteFileSystem).OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	_, err = s3fsys.(fs.ReadWriterFileSystem).OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.WriteFileSystem).MakeDir("/dir", 0), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.WriteFileSystem).Remove("/file"), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.WriteAllFileSystem).WriteAll(ctx, "/file", nil, 0), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "/a", "/b"), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.TouchFileSystem).Touch("/file", 0), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, s3fsys.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/dir"), fs.ErrReadOnlyFileSystem)
}

// TestEmptyPath verifies that every method rejects an empty path
// with fs.ErrEmptyPath without a request.
func TestEmptyPath(t *testing.T) {
	client := s3.New(s3.Options{Region: "us-east-1"})
	s3fsys, err := s3fs.New(client, "s3fs-empty-path-test-bucket", nil)
	require.NoError(t, err)
	ctx := t.Context()

	_, err = s3fsys.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = s3fsys.OpenReader("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = s3fsys.(fs.WriteFileSystem).OpenWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = s3fsys.(fs.ReadWriterFileSystem).OpenReadWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = s3fsys.(fs.ReadAllFileSystem).ReadAll(ctx, "")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.ListDir(ctx, "", nil, nil), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.ListDirRecursiveFileSystem).ListDirRecursive(ctx, "", nil, nil), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.WriteFileSystem).MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.WriteFileSystem).Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.WriteAllFileSystem).WriteAll(ctx, "", nil, 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "", "/b"), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.TouchFileSystem).Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, s3fsys.(fs.RemoveAllFileSystem).RemoveAll(ctx, ""), fs.ErrEmptyPath)

	// The root directory is a directory without any request
	info, err := s3fsys.Stat("/")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	_, err = s3fsys.OpenReader("/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	_, err = s3fsys.(fs.ReadAllFileSystem).ReadAll(ctx, "/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.ErrorIs(t, s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "/", "/b"), fs.ErrIsDirectory{})
	assert.NoError(t, s3fsys.(fs.CopyFileSystem).CopyFile(ctx, "/same", "/same"), "copying onto itself is a no-op")
	assert.NoError(t, s3fsys.(fs.TouchFileSystem).Touch("/", 0), "Touch of the root does nothing")
}
