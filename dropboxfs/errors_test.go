package dropboxfs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// notFoundMetadataErr builds the typed error the Dropbox SDK returns from
// get_metadata when a path does not exist.
func notFoundMetadataErr() files.GetMetadataAPIError {
	return files.GetMetadataAPIError{
		EndpointError: &files.GetMetadataError{
			Path: &files.LookupError{
				Tagged: dropbox.Tagged{Tag: files.LookupErrorNotFound},
			},
		},
	}
}

func TestIsNotExistError(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.False(t, isNotExistError(nil))
	})

	t.Run("typed not_found", func(t *testing.T) {
		assert.True(t, isNotExistError(notFoundMetadataErr()),
			"a get_metadata not_found error means the path does not exist")
	})

	t.Run("typed wrapped not_found", func(t *testing.T) {
		wrapped := fmt.Errorf("context: %w", notFoundMetadataErr())
		assert.True(t, isNotExistError(wrapped))
	})

	t.Run("typed other lookup tag is not not-found", func(t *testing.T) {
		err := files.GetMetadataAPIError{
			EndpointError: &files.GetMetadataError{
				Path: &files.LookupError{
					Tagged: dropbox.Tagged{Tag: files.LookupErrorRestrictedContent},
				},
			},
		}
		assert.False(t, isNotExistError(err),
			"restricted_content is not a missing file")
	})

	t.Run("transient errors are not not-found", func(t *testing.T) {
		// These mimic auth / rate-limit / network failures. None must be
		// misclassified as "does not exist".
		for _, err := range []error{
			errors.New("too_many_requests"),
			errors.New("expired_access_token"),
			errors.New("dial tcp: connection refused"),
			errors.New("500 Internal Server Error"),
		} {
			assert.Falsef(t, isNotExistError(err), "%v must not be treated as not-found", err)
		}
	})

	t.Run("string fallback path/not_found", func(t *testing.T) {
		// Other Dropbox routes stringify their LookupError into the summary.
		assert.True(t, isNotExistError(errors.New("path/not_found/.")))
	})
}

// TestClosedFileSystem verifies that after Close every method that uses the
// Dropbox API returns fs.ErrFileSystemClosed (or false for Exists) instead of
// dereferencing a closed client.
func TestClosedFileSystem(t *testing.T) {
	// A real (offline) client is fine: closed methods short-circuit before any
	// network call, so no token or connectivity is required.
	dbfs := NewAndRegister("offline-token", time.Minute, false).(*fileSystem)

	require.True(t, fs.IsRegistered(dbfs), "filesystem should be registered before Close")

	require.NoError(t, dbfs.Close())

	// Regression: Close must unregister even though ID() was never called.
	// The previous implementation used id=="" as the closed flag and would
	// skip Unregister for a filesystem whose account ID had not been fetched.
	assert.False(t, fs.IsRegistered(dbfs), "Close must unregister the filesystem")

	// Close is idempotent.
	assert.NoError(t, dbfs.Close())

	ctx := t.Context()

	assert.False(t, dbfs.Exists("/file"), "Exists must be false on a closed filesystem")

	_, err := dbfs.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.ID()
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.WriteAll(ctx, "/file", []byte("x"), nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.MakeDir("/dir", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.Move("/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.CopyFile(ctx, "/a", "/b", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.OpenWriter("/file", nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.ListDirInfo(ctx, "/dir", func(*fs.FileInfo) error { return nil }, nil)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
