package fsimpl

import (
	"errors"
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
