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

	t.Run("untyped not_found strings are not not-found", func(t *testing.T) {
		// Only typed API errors are trusted, never error message contents.
		assert.False(t, isNotExistError(errors.New("path/not_found/.")))
	})

	t.Run("typed download not_found", func(t *testing.T) {
		err := files.DownloadAPIError{
			EndpointError: &files.DownloadError{
				Path: &files.LookupError{
					Tagged: dropbox.Tagged{Tag: files.LookupErrorNotFound},
				},
			},
		}
		assert.True(t, isNotExistError(err))
	})
}

func TestIsConflictError(t *testing.T) {
	err := files.CreateFolderAPIError{
		EndpointError: &files.CreateFolderError{
			Path: &files.WriteError{
				Tagged: dropbox.Tagged{Tag: files.WriteErrorConflict},
			},
		},
	}
	assert.True(t, isConflictError(err), "create_folder conflict means the path exists")
	assert.False(t, isConflictError(errors.New("conflict")), "untyped errors are not conflicts")
}

// TestClosedFileSystem verifies that after Close every method that uses the
// Dropbox API returns fs.ErrFileSystemClosed instead of dereferencing a
// closed client.
func TestClosedFileSystem(t *testing.T) {
	// A real (offline) client is fine: closed methods short-circuit before any
	// network call, so no token or connectivity is required.
	dbfs := newFileSystem("dbid:test", files.New(dropbox.Config{Token: "offline-token"}), time.Minute, false)
	fs.Register(dbfs)

	require.True(t, fs.IsRegistered(dbfs), "filesystem should be registered before Close")

	require.NoError(t, dbfs.Close())

	assert.False(t, fs.IsRegistered(dbfs), "Close must unregister the filesystem")

	// Close is idempotent.
	assert.NoError(t, dbfs.Close())

	ctx := t.Context()

	exists, err := dbfs.Exists("/file")
	assert.False(t, exists, "Exists must be false on a closed filesystem")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.WriteAll(ctx, "/file", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.MakeDir("/dir", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.Remove("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.Move("/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.CopyFile(ctx, "/a", "/b")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	_, err = dbfs.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	err = dbfs.RemoveAll(ctx, "/dir")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}
