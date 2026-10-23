package fsimpl

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadWriteAllSeekCloser_CloseCallback verifies that the close callback
// passed to the constructor is honored: it must be called on Close even when
// nothing was modified (otherwise the underlying file handle leaks), and it
// must be called at most once.
func TestReadWriteAllSeekCloser_CloseCallback(t *testing.T) {
	read := func() ([]byte, error) { return []byte("data"), nil }
	write := func([]byte) error { return nil }

	t.Run("CalledWhenNotModified", func(t *testing.T) {
		closed := 0
		rw := NewReadWriteAllSeekCloser(read, write, func() error { closed++; return nil })

		// Read only, never write.
		buf := make([]byte, 4)
		_, err := rw.Read(buf)
		require.NoError(t, err)

		require.NoError(t, rw.Close())
		assert.Equal(t, 1, closed, "close callback must run even without modifications")
	})

	t.Run("CalledWithoutAnyIO", func(t *testing.T) {
		closed := 0
		rw := NewReadWriteAllSeekCloser(read, write, func() error { closed++; return nil })

		// Close immediately, without any read/write/seek (buffer never loaded).
		require.NoError(t, rw.Close())
		assert.Equal(t, 1, closed, "close callback must run even when the buffer was never loaded")
	})

	t.Run("CalledWhenModified", func(t *testing.T) {
		closed := 0
		var written []byte
		rw := NewReadWriteAllSeekCloser(
			read,
			func(data []byte) error { written = data; return nil },
			func() error { closed++; return nil },
		)

		_, err := rw.Write([]byte("XXXX"))
		require.NoError(t, err)

		require.NoError(t, rw.Close())
		assert.Equal(t, 1, closed, "close callback must run after a write-back")
		assert.Equal(t, "XXXX", string(written), "modified data must be written back")
	})

	t.Run("CalledAtMostOnce", func(t *testing.T) {
		closed := 0
		rw := NewReadWriteAllSeekCloser(read, write, func() error { closed++; return nil })

		require.NoError(t, rw.Close())
		require.NoError(t, rw.Close())
		require.NoError(t, rw.Close())
		assert.Equal(t, 1, closed, "close callback must not run more than once")
	})

	t.Run("JoinsWriteAndCloseErrors", func(t *testing.T) {
		writeErr := errors.New("write failed")
		closeErr := errors.New("close failed")
		rw := NewReadWriteAllSeekCloser(
			read,
			func([]byte) error { return writeErr },
			func() error { return closeErr },
		)

		_, err := rw.Write([]byte("XXXX"))
		require.NoError(t, err)

		err = rw.Close()
		assert.ErrorIs(t, err, writeErr, "write-back error must be reported")
		assert.ErrorIs(t, err, closeErr, "close error must be reported")
	})

	t.Run("NilCloseIsSafe", func(t *testing.T) {
		rw := NewReadWriteAllSeekCloser(read, write, nil)
		_, err := rw.Write([]byte("XXXX"))
		require.NoError(t, err)
		assert.NoError(t, rw.Close(), "a nil close callback must be a safe no-op")
	})
}

// TestReadWriteAllSeekCloser_AfterClose verifies that every method fails
// with os.ErrClosed after Close, instead of accepting writes that are
// never written back.
func TestReadWriteAllSeekCloser_AfterClose(t *testing.T) {
	writes := 0
	rw := NewReadWriteAllSeekCloser(
		func() ([]byte, error) { return []byte("x"), nil },
		func([]byte) error { writes++; return nil },
		nil,
	)
	_, err := rw.Write([]byte("y"))
	require.NoError(t, err)
	require.NoError(t, rw.Close())
	assert.Equal(t, 1, writes)

	n, err := rw.Write([]byte("z"))
	assert.ErrorIs(t, err, os.ErrClosed, "Write after Close")
	assert.Zero(t, n)
	_, err = rw.WriteAt([]byte("z"), 0)
	assert.ErrorIs(t, err, os.ErrClosed, "WriteAt after Close")
	_, err = rw.Read(make([]byte, 1))
	assert.ErrorIs(t, err, os.ErrClosed, "Read after Close")
	_, err = rw.ReadAt(make([]byte, 1), 0)
	assert.ErrorIs(t, err, os.ErrClosed, "ReadAt after Close")
	_, err = rw.Seek(0, io.SeekStart)
	assert.ErrorIs(t, err, os.ErrClosed, "Seek after Close")
	require.NoError(t, rw.Close())
	assert.Equal(t, 1, writes, "a second Close must not write again")
}

// TestReadWriteAllSeekCloser_Abort verifies that Abort discards the
// modifications, still releases the handle, and turns Close into a no-op.
func TestReadWriteAllSeekCloser_Abort(t *testing.T) {
	writes, closes := 0, 0
	rw := NewReadWriteAllSeekCloser(
		func() ([]byte, error) { return []byte("data"), nil },
		func([]byte) error { writes++; return nil },
		func() error { closes++; return nil },
	)
	var _ interface {
		io.WriteCloser
		Abort() error
	} = rw // the method set of fs.WriteAborter

	_, err := rw.Write([]byte("partial"))
	require.NoError(t, err)
	require.NoError(t, rw.Abort())
	require.NoError(t, rw.Close(), "Close after Abort is a no-op")
	require.NoError(t, rw.Abort(), "Abort after Abort is a no-op")
	assert.Zero(t, writes, "aborted modifications must not be written back")
	assert.Equal(t, 1, closes, "Abort must release the handle exactly once")

	_, err = rw.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrClosed, "Write after Abort")

	closeErr := errors.New("close failed")
	rw = NewReadWriteAllSeekCloser(
		func() ([]byte, error) { return nil, nil },
		func([]byte) error { return nil },
		func() error { return closeErr },
	)
	assert.ErrorIs(t, rw.Abort(), closeErr, "Abort returns the error of the close callback")
}
