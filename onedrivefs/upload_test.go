package onedrivefs

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenWriter_NoTempFile checks that a writer whose temporary file
// can't be created fails in Write and Close without uploading the part
// it kept in memory, which would replace the file with its beginning.
func TestOpenWriter_NoTempFile(t *testing.T) {
	setTempDir(t, filepath.Join(t.TempDir(), "missing"))
	f, g := newTestFS(t)
	g.put("/file.bin", []byte("old"))

	w, err := f.OpenWriter("/file.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, testMaxSimple))
	require.NoError(t, err, "the first part fits into memory")
	_, err = w.Write([]byte("more"))
	require.Error(t, err, "the temporary file can't be created")
	_, err = w.Write([]byte("more"))
	require.Error(t, err, "the error sticks")
	require.Error(t, w.Close())
	assert.Equal(t, "old", string(g.get("/file.bin").data))
	assert.Zero(t, g.callCount("PutContent"))
	assert.Zero(t, g.callCount("CreateUploadSession"))
}

// setTempDir makes os.TempDir return dir for the test:
// it reads TMPDIR on Unix and TMP or TEMP on Windows.
func setTempDir(t *testing.T, dir string) {
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
}

// TestUploadSession_NotCommitted checks that an upload whose last
// fragment the server doesn't answer with the item is an error,
// and that its session is canceled.
func TestUploadSession_NotCommitted(t *testing.T) {
	f, g := newTestFS(t)
	g.put("/file.bin", []byte("old"))
	data := make([]byte, testMaxSimple+1) // two fragments
	g.setFailNext("UploadFragment", fakeFailure{}, fakeFailure{status: http.StatusAccepted})

	err := f.WriteAll(t.Context(), "/file.bin", data, 0)
	assert.ErrorContains(t, err, "answered the last fragment with 202")
	assert.Equal(t, "old", string(g.get("/file.bin").data))
	assert.Equal(t, 1, g.callCount("CancelUploadSession"))

	// An expired session is not a missing file
	g.setFailNext("UploadFragment", fakeFailure{status: http.StatusNotFound, code: "itemNotFound"})
	err = f.WriteAll(t.Context(), "/file.bin", data, 0)
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	assert.NotErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, "old", string(g.get("/file.bin").data))
}

// TestOpenWriter checks that a writer keeps a small file in memory,
// writes a large one to a temporary file, which Close and Abort remove,
// and that an aborted or failed upload doesn't change the file.
func TestOpenWriter(t *testing.T) {
	tempDir := t.TempDir()
	setTempDir(t, tempDir)
	f, g := newTestFS(t)
	g.put("/file.bin", []byte("old"))
	data := make([]byte, testMaxSimple+2*testFragmentSize+10)
	for i := range data {
		data[i] = byte(i)
	}

	write := func(t *testing.T) *uploadWriter {
		t.Helper()
		w, err := f.OpenWriter("/file.bin", 0)
		require.NoError(t, err)
		for chunk := range slices.Chunk(data, 100_000) {
			_, err := w.Write(chunk)
			require.NoError(t, err)
		}
		uw := w.(*uploadWriter)
		require.NotNil(t, uw.spool, "a file larger than a simple upload is written to a temporary file")
		return uw
	}
	tempFiles := func() []os.DirEntry {
		entries, err := os.ReadDir(tempDir)
		require.NoError(t, err)
		return entries
	}

	w := write(t)
	require.NoError(t, w.Abort())
	assert.Empty(t, tempFiles(), "Abort must remove the temporary file")
	assert.Equal(t, "old", string(g.get("/file.bin").data))
	assert.Zero(t, g.callCount("CreateUploadSession"))

	w = write(t)
	g.setFailNext("UploadFragment", fakeFailure{}, fakeFailure{status: http.StatusInternalServerError})
	assert.Equal(t, http.StatusInternalServerError, statusOf(w.Close()))
	assert.Empty(t, tempFiles(), "Close must remove the temporary file")
	assert.Equal(t, "old", string(g.get("/file.bin").data), "a failed upload must not change the file")
	assert.Equal(t, 1, g.callCount("CancelUploadSession"), "the session of a failed upload must be canceled")

	w = write(t)
	require.NoError(t, w.Close())
	assert.Empty(t, tempFiles(), "Close must remove the temporary file")
	assert.Equal(t, data, g.get("/file.bin").data)
	assert.NoError(t, w.Close(), "Close is idempotent")
	_, err := w.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrClosed)
}
