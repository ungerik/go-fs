package dropboxfs

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestUpload_Session is the regression test for uploads above
// the limit of one upload request, which failed.
func TestUpload_Session(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, &Options{Mute: true})
	data := bytes.Repeat([]byte("0123456789abcdef"), (2*testMaxUpload+100)/16)

	// WriteAll
	require.NoError(t, dbfs.WriteAll(ctx, "/large.bin", data, 0))
	assert.Equal(t, data, client.entries["/large.bin"].data)
	assert.Zero(t, client.callCount("Upload"))
	assert.Equal(t, 1, client.callCount("UploadSessionStart"))
	assert.Equal(t, 1, client.callCount("UploadSessionFinish"))
	assert.Equal(t, len(data)/testChunkSize-1, client.callCount("UploadSessionAppendV2"))
	assert.Equal(t, 1, client.muted, "the commit of the session must be muted")

	// OpenWriter streams chunks while writing
	w, err := dbfs.OpenWriter("/written.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(data[:testChunkSize+1])
	require.NoError(t, err)
	assert.Equal(t, 2, client.callCount("UploadSessionStart"), "the first chunk is uploaded while writing")
	_, err = w.Write(data[testChunkSize+1:])
	require.NoError(t, err)
	require.NoError(t, w.Close())
	assert.Equal(t, data, client.entries["/written.bin"].data)
	assert.Zero(t, client.callCount("Upload"))

	// A file that fits into a chunk is uploaded with one request
	require.NoError(t, dbfs.WriteAll(ctx, "/small.txt", []byte("small"), 0))
	w, err = dbfs.OpenWriter("/small2.txt", 0)
	require.NoError(t, err)
	_, err = w.Write(data[:testChunkSize])
	require.NoError(t, err)
	require.NoError(t, w.Close())
	assert.Equal(t, 2, client.callCount("Upload"))
	assert.Equal(t, 2, client.callCount("UploadSessionStart"))
	assert.Equal(t, 4, client.muted)

	// Cross file system copies use OpenWriter
	mem, err := fs.NewMemFileSystem(nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(mem))
	t.Cleanup(func() { _ = mem.Close() })
	src := mem.RootDir().Join("src.bin")
	require.NoError(t, src.WriteAll(ctx, data))
	require.NoError(t, fs.CopyFile(ctx, src, dbfs.RootDir().Join("copied.bin")))
	assert.Equal(t, data, client.entries["/copied.bin"].data)
	assert.Equal(t, 3, client.callCount("UploadSessionStart"))
}

// TestUploadWriter_SessionReplaces checks that a writer whose data needs
// an upload session replaces an existing file on Close, like WriteAll,
// and doesn't change it before.
func TestUploadWriter_SessionReplaces(t *testing.T) {
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(t.Context(), "/existing.bin", []byte("old"), 0))
	data := bytes.Repeat([]byte("new data"), 3*testChunkSize/8)

	w, err := dbfs.OpenWriter("/existing.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.Equal(t, 1, client.callCount("UploadSessionStart"), "the data must be uploaded with a session")
	assert.Equal(t, "old", string(client.entries["/existing.bin"].data), "the file must not change before Close")
	require.NoError(t, w.Close())
	assert.Equal(t, 1, client.callCount("UploadSessionFinish"))
	assert.Equal(t, data, client.entries["/existing.bin"].data, "Close must replace the existing file")
}

// TestUploadWriter_Abort checks that Abort never commits an upload session,
// so an existing file is not changed and a new file is not created.
func TestUploadWriter_Abort(t *testing.T) {
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(t.Context(), "/keep.bin", []byte("keep"), 0))

	for i, filePath := range []string{"/keep.bin", "/new.bin"} {
		w, err := dbfs.OpenWriter(filePath, 0)
		require.NoError(t, err)
		aborter, ok := w.(fs.WriteAborter)
		require.True(t, ok, "the writer implements fs.WriteAborter")
		_, err = w.Write(make([]byte, 3*testChunkSize))
		require.NoError(t, err)
		require.Equal(t, i+1, client.callCount("UploadSessionStart"), "%s must be uploaded with a session", filePath)
		require.NoError(t, aborter.Abort())
		require.NoError(t, w.Close(), "Close after Abort is a no-op")
		_, err = w.Write([]byte("x"))
		assert.ErrorIs(t, err, os.ErrClosed)
	}
	assert.Zero(t, client.callCount("UploadSessionFinish"), "Abort must not commit a session")
	assert.Equal(t, 1, client.callCount("Upload"), "only WriteAll of /keep.bin uploads")
	assert.Equal(t, "keep", string(client.entries["/keep.bin"].data), "Abort must not change an existing file")
	assert.NotContains(t, client.entries, "/new.bin", "Abort must not create a file")
}

// TestUploadWriter_Errors checks that a failed chunk upload
// is reported by Write and Close, and that writing onto a folder
// is an fs.ErrIsDirectory, also for upload sessions.
func TestUploadWriter_Errors(t *testing.T) {
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.MakeDir("/dir", 0))

	err := dbfs.WriteAll(t.Context(), "/dir", make([]byte, 2*testMaxUpload), 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "upload session onto a folder")
	err = dbfs.WriteAll(t.Context(), "/dir", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "upload onto a folder")
	err = dbfs.WriteAll(t.Context(), "/", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "upload onto the root")
	_, err = dbfs.OpenWriter("/", 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "writer for the root")

	w, err := dbfs.OpenWriter("/failing.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, testChunkSize))
	require.NoError(t, err)
	client.setFailNext("UploadSessionStart", os.ErrDeadlineExceeded)
	_, err = w.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	_, err = w.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded, "the error sticks")
	assert.ErrorIs(t, w.Close(), os.ErrDeadlineExceeded)
	assert.NotContains(t, client.entries, "/failing.bin")

	// A writer closed after the file system
	w, err = dbfs.OpenWriter("/late.txt", 0)
	require.NoError(t, err)
	require.NoError(t, dbfs.Close())
	assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
}
