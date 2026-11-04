package onedrivefs

import (
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenWriter checks that a writer keeps a small file in memory,
// writes a large one to a temporary file, which Close and Abort remove,
// and that an aborted or failed upload doesn't change the file.
func TestOpenWriter(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
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
