package dropboxfs

import (
	"errors"
	"fmt"
	"os"
	"testing"

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

// TestIsConflictError checks the error types of the V2 endpoints, which
// differ from the ones of the V1 endpoints that were checked before.
func TestIsConflictError(t *testing.T) {
	err := files.CreateFolderV2APIError{
		EndpointError: &files.CreateFolderError{
			Path: &files.WriteError{
				Tagged: dropbox.Tagged{Tag: files.WriteErrorConflict},
			},
		},
	}
	assert.True(t, isConflictError(err), "create_folder_v2 conflict means the path exists")
	assert.False(t, isConflictError(files.CreateFolderV2APIError{}), "no endpoint error")
	assert.False(t, isConflictError(errors.New("conflict")), "untyped errors are not conflicts")

	move := files.MoveV2APIError{EndpointError: &files.RelocationError{
		Tagged: dropbox.Tagged{Tag: files.RelocationErrorTo},
		To:     conflictError(files.WriteConflictErrorFolder),
	}}
	assert.Equal(t, files.WriteConflictErrorFolder, conflictTag(move))
	assert.Equal(t, files.WriteConflictErrorFolder, conflictTag(fmt.Errorf("wrapped: %w", move)))
}

// TestWrapErr checks the mapping of the typed Dropbox errors to the fs errors.
func TestWrapErr(t *testing.T) {
	dbfs := newFileSystem("dbid:wrap", nil, nil)
	for _, c := range []struct {
		err    error
		target error
	}{
		{files.DeleteV2APIError{EndpointError: &files.DeleteError{PathLookup: lookupError(files.LookupErrorNotFound)}}, os.ErrNotExist},
		{files.ListFolderContinueAPIError{EndpointError: &files.ListFolderContinueError{Path: lookupError(files.LookupErrorNotFound)}}, os.ErrNotExist},
		{files.DownloadAPIError{EndpointError: &files.DownloadError{Path: lookupError(files.LookupErrorNotFile)}}, fs.ErrIsDirectory{}},
		{files.ListFolderAPIError{EndpointError: &files.ListFolderError{Path: lookupError(files.LookupErrorNotFolder)}}, fs.ErrIsNotDirectory{}},
		{files.UploadAPIError{EndpointError: &files.UploadError{Path: &files.UploadWriteFailed{Reason: conflictError(files.WriteConflictErrorFolder)}}}, fs.ErrIsDirectory{}},
		{files.UploadSessionFinishAPIError{EndpointError: &files.UploadSessionFinishError{Path: conflictError(files.WriteConflictErrorFileAncestor)}}, fs.ErrIsNotDirectory{}},
		{files.CopyV2APIError{EndpointError: &files.RelocationError{Tagged: dropbox.Tagged{Tag: files.RelocationErrorTo}, To: conflictError(files.WriteConflictErrorFile)}}, os.ErrExist},
		{files.CreateFolderV2APIError{EndpointError: &files.CreateFolderError{Path: &files.WriteError{Tagged: dropbox.Tagged{Tag: files.WriteErrorNoWritePermission}}}}, os.ErrPermission},
	} {
		assert.ErrorIs(t, dbfs.wrapErr("/x", c.err), c.target, "%#v", c.err)
	}
	transient := errors.New("too_many_requests")
	assert.Equal(t, transient, dbfs.wrapErr("/x", transient))
	assert.NoError(t, dbfs.wrapErr("/x", nil))

	intoItself := files.MoveV2APIError{EndpointError: &files.RelocationError{Tagged: dropbox.Tagged{Tag: files.RelocationErrorCantMoveFolderIntoItself}}}
	assert.ErrorIs(t, dbfs.wrapRelocationErr("/a", "/a/b", intoItself), os.ErrInvalid)
	fromMissing := files.MoveV2APIError{EndpointError: &files.RelocationError{Tagged: dropbox.Tagged{Tag: files.RelocationErrorFromLookup}, FromLookup: lookupError(files.LookupErrorNotFound)}}
	err := dbfs.wrapRelocationErr("/a", "/b", fromMissing)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.ErrorContains(t, err, "/a", "the error is about the source")
}

// TestWrapRelocationErr_FromWrite is the regression test for a move whose
// source can't be deleted for a missing write permission, which was not
// mapped to fs.ErrPermission, because wrapErr only reads the write error
// of the destination.
func TestWrapRelocationErr_FromWrite(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(ctx, "/a.txt", []byte("a"), 0))
	fromWrite := func(write *files.WriteError) files.MoveV2APIError {
		return files.MoveV2APIError{EndpointError: &files.RelocationError{
			Tagged:    dropbox.Tagged{Tag: files.RelocationErrorFromWrite},
			FromWrite: write,
		}}
	}

	client.setFailNext("MoveV2", fromWrite(&files.WriteError{Tagged: dropbox.Tagged{Tag: files.WriteErrorNoWritePermission}}))
	err := dbfs.Move(ctx, "/a.txt", "/b.txt")
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.ErrorContains(t, err, "/a.txt", "the error is about the source")
	assert.Equal(t, "a", string(client.entries["/a.txt"].data))

	// A conflict of the source is no conflict of the destination,
	// which relocate would replace
	conflict := fromWrite(conflictError(files.WriteConflictErrorFile))
	assert.Empty(t, conflictTag(conflict))
	assert.Equal(t, error(conflict), dbfs.wrapRelocationErr("/a.txt", "/b.txt", conflict), "other errors of the source are returned unchanged")
}
