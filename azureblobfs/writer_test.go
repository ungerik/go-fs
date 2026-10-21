package azureblobfs

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// fakeUpload records what an uploadWriter uploads.
type fakeUpload struct {
	calls     atomic.Int32
	committed atomic.Value // []byte of the last upload that read io.EOF
	err       error        // returned instead of reading the body
	readErr   chan error   // receives the error that ended reading
}

func newFakeUpload() *fakeUpload {
	return &fakeUpload{readErr: make(chan error, 2)}
}

func (u *fakeUpload) upload(ctx context.Context, body io.Reader) error {
	u.calls.Add(1)
	if u.err != nil {
		return u.err
	}
	data, err := io.ReadAll(body)
	u.readErr <- err
	if err != nil {
		return err
	}
	u.committed.Store(data)
	return nil
}

func (u *fakeUpload) data() []byte {
	data, _ := u.committed.Load().([]byte)
	return data
}

func isOpen() error { return nil }

func TestUploadWriter(t *testing.T) {
	t.Run("Close", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		_, err := w.Write([]byte("hello "))
		require.NoError(t, err)
		_, err = w.Write([]byte("world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		assert.Equal(t, "hello world", string(u.data()))
		assert.Equal(t, int32(1), u.calls.Load())

		assert.NoError(t, w.Close(), "Close after Close")
		assert.NoError(t, w.Abort(), "Abort after Close")
		_, err = w.Write([]byte("x"))
		assert.ErrorIs(t, err, os.ErrClosed, "Write after Close")
		assert.Equal(t, "hello world", string(u.data()))
	})

	t.Run("CloseWithoutWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		require.NoError(t, w.Close())
		assert.Equal(t, int32(1), u.calls.Load(), "Close without Write must upload an empty file")
		assert.NotNil(t, u.committed.Load())
		assert.Empty(t, u.data())
	})

	t.Run("AbortWithoutWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		require.NoError(t, w.Abort())
		require.NoError(t, w.Close(), "Close after Abort")
		assert.Zero(t, u.calls.Load(), "Abort without Write must not upload")
	})

	t.Run("AbortAfterWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		_, err := w.Write([]byte("aborted data"))
		require.NoError(t, err)
		require.NoError(t, w.Abort())
		assert.ErrorIs(t, <-u.readErr, errAborted, "the upload must read the abort error")
		assert.Nil(t, u.committed.Load(), "Abort must not commit")
		require.NoError(t, w.Close(), "Close after Abort")
		_, err = w.Write([]byte("x"))
		assert.ErrorIs(t, err, os.ErrClosed, "Write after Abort")
		assert.Nil(t, u.committed.Load(), "Close after Abort must not commit")
	})

	t.Run("UploadError", func(t *testing.T) {
		u := newFakeUpload()
		u.err = errors.New("upload failed")
		w := newUploadWriter(u.upload, isOpen)
		var err error
		// The failed upload doesn't read, the error reaches the next Writes
		for range 3 {
			if _, err = w.Write([]byte("data")); err != nil {
				break
			}
		}
		assert.ErrorIs(t, err, u.err, "Write after the upload failed")
		assert.ErrorIs(t, w.Close(), u.err, "Close returns the upload error")
	})

	t.Run("FileSystemClosed", func(t *testing.T) {
		u := newFakeUpload()
		var closed atomic.Bool
		checkOpen := func() error {
			if closed.Load() {
				return fs.ErrFileSystemClosed
			}
			return nil
		}
		w := newUploadWriter(u.upload, checkOpen)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		closed.Store(true)
		assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
		assert.ErrorIs(t, <-u.readErr, errAborted, "Close after the file system was closed must abort")
		assert.Nil(t, u.committed.Load())

		w = newUploadWriter(u.upload, checkOpen)
		_, err = w.Write([]byte("data"))
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "first Write after the file system was closed")
		assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
	})
}
