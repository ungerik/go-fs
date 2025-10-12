package fsimpl

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadWriteAllSeekCloser(t *testing.T) {
	t.Run("ReadInitialContent", func(t *testing.T) {
		initialData := []byte("Hello, World!")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		buf := make([]byte, len(initialData))
		n, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, len(initialData), n, "Should read all bytes")
		assert.Equal(t, initialData, buf, "Read content should match initial data")

		err = rw.Close()
		require.NoError(t, err, "Close should not error")
	})

	t.Run("WriteAndReadBack", func(t *testing.T) {
		initialData := []byte("Hello, World!")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		// Seek to position 7 (after "Hello, ")
		pos, err := rw.Seek(7, io.SeekStart)
		require.NoError(t, err, "Seek should not error")
		assert.Equal(t, int64(7), pos, "Should seek to position 7")

		// Write "Test!"
		newText := []byte("Test!")
		n, err := rw.Write(newText)
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, len(newText), n, "Should write all bytes")

		// Seek back to start and read
		_, err = rw.Seek(0, io.SeekStart)
		require.NoError(t, err, "Seek to start should not error")

		buf := make([]byte, 20)
		n, err = rw.Read(buf)
		if err != nil && err != io.EOF {
			require.NoError(t, err, "Read should not error")
		}
		// Original: "Hello, World!" (13 bytes)
		// After writing "Test!" at position 7: "Hello, Test!!" (14 bytes - expanded)
		assert.Equal(t, "Hello, Test!!", string(buf[:n]), "Should read modified content")

		// Close and verify write-back
		err = rw.Close()
		require.NoError(t, err, "Close should not error")
		assert.Equal(t, "Hello, Test!!", string(mockFile.data), "Written data should match modified content")
	})

	t.Run("WriteAtAndReadAt", func(t *testing.T) {
		initialData := []byte("0123456789")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)
		defer rw.Close()

		// WriteAt position 3
		n, err := rw.WriteAt([]byte("ABC"), 3)
		require.NoError(t, err, "WriteAt should not error")
		assert.Equal(t, 3, n, "Should write 3 bytes")

		// ReadAt position 0
		buf := make([]byte, 10)
		n, err = rw.ReadAt(buf, 0)
		require.NoError(t, err, "ReadAt should not error")
		assert.Equal(t, "012ABC6789", string(buf[:n]), "Should read modified content at position")

		err = rw.Close()
		require.NoError(t, err, "Close should not error")
		assert.Equal(t, "012ABC6789", string(mockFile.data), "Written data should match modified content")
	})

	t.Run("SeekOperations", func(t *testing.T) {
		initialData := []byte("0123456789")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)
		defer rw.Close()

		// Seek from start
		pos, err := rw.Seek(5, io.SeekStart)
		require.NoError(t, err, "Seek from start should not error")
		assert.Equal(t, int64(5), pos, "Should be at position 5")

		// Seek from current
		pos, err = rw.Seek(2, io.SeekCurrent)
		require.NoError(t, err, "Seek from current should not error")
		assert.Equal(t, int64(7), pos, "Should be at position 7")

		// Seek from end
		pos, err = rw.Seek(-3, io.SeekEnd)
		require.NoError(t, err, "Seek from end should not error")
		assert.Equal(t, int64(7), pos, "Should be at position 7 (10-3)")

		// Read from current position
		buf := make([]byte, 3)
		n, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, "789", string(buf[:n]), "Should read from position 7")
	})

	t.Run("NoWriteNoWriteback", func(t *testing.T) {
		initialData := []byte("Hello, World!")
		mockFile := &mockReadWriteAllFile{data: initialData}
		originalData := make([]byte, len(initialData))
		copy(originalData, initialData)

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		// Only read, no write
		buf := make([]byte, 5)
		_, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")

		err = rw.Close()
		require.NoError(t, err, "Close should not error")
		assert.Equal(t, originalData, mockFile.data, "Data should not be modified if no writes occurred")
	})

	t.Run("ExpandBuffer", func(t *testing.T) {
		initialData := []byte("Hello")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		// Write beyond the initial size
		_, err := rw.Seek(0, io.SeekEnd)
		require.NoError(t, err, "Seek to end should not error")

		n, err := rw.Write([]byte(", World!"))
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, 8, n, "Should write 8 bytes")

		err = rw.Close()
		require.NoError(t, err, "Close should not error")
		assert.Equal(t, "Hello, World!", string(mockFile.data), "Buffer should expand and contain appended data")
	})

	t.Run("LazyLoading", func(t *testing.T) {
		initialData := []byte("Hello, World!")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		// Buffer should not be loaded yet
		assert.Equal(t, 0, mockFile.readCount, "ReadAll should not be called on construction")

		// First read should trigger loading
		buf := make([]byte, 5)
		_, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, 1, mockFile.readCount, "ReadAll should be called once on first read")

		// Second read should not trigger loading again
		_, err = rw.Read(buf)
		require.NoError(t, err, "Second read should not error")
		assert.Equal(t, 1, mockFile.readCount, "ReadAll should still be called only once")

		rw.Close()
	})

	t.Run("InvalidateBuffer", func(t *testing.T) {
		initialData := []byte("Hello, World!")
		mockFile := &mockReadWriteAllFile{data: initialData}

		rw := NewReadWriteAllSeekCloser(mockFile.ReadAll, mockFile.WriteAll)

		// Read some data
		buf := make([]byte, 5)
		_, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, "Hello", string(buf), "Should read initial content")

		// Modify the underlying file
		mockFile.data = []byte("Updated content!")

		// Invalidate buffer
		rw.InvalidateBuffer()

		// Next read should get new content
		buf = make([]byte, 7)
		_, err = rw.Read(buf)
		require.NoError(t, err, "Read after invalidate should not error")
		assert.Equal(t, "Updated", string(buf), "Should read updated content after invalidate")

		rw.Close()
	})

	t.Run("FunctionPointerConstructor", func(t *testing.T) {
		// Test using function pointers directly
		data := []byte("Hello, World!")

		readAll := func() ([]byte, error) {
			result := make([]byte, len(data))
			copy(result, data)
			return result, nil
		}

		writeAll := func(newData []byte) error {
			data = make([]byte, len(newData))
			copy(data, newData)
			return nil
		}

		rw := NewReadWriteAllSeekCloser(readAll, writeAll)

		// Read initial content
		buf := make([]byte, 5)
		n, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, 5, n, "Should read 5 bytes")
		assert.Equal(t, "Hello", string(buf), "Should read initial content")

		// Write some data
		_, err = rw.Seek(7, io.SeekStart)
		require.NoError(t, err, "Seek should not error")

		n, err = rw.Write([]byte("Test!"))
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, 5, n, "Should write 5 bytes")

		// Close and verify
		err = rw.Close()
		require.NoError(t, err, "Close should not error")
		assert.Equal(t, "Hello, Test!!", string(data), "Data should be written back")
	})

	t.Run("NilCloseFunction", func(t *testing.T) {
		// Test with nil close function
		data := []byte("Hello, World!")

		readAll := func() ([]byte, error) {
			result := make([]byte, len(data))
			copy(result, data)
			return result, nil
		}

		writeAll := func(newData []byte) error {
			data = make([]byte, len(newData))
			copy(data, newData)
			return nil
		}

		rw := NewReadWriteAllSeekCloser(readAll, writeAll)

		// Read and write
		buf := make([]byte, 5)
		_, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")

		_, err = rw.Write([]byte(" Test"))
		require.NoError(t, err, "Write should not error")

		// Close should not error even with nil close function
		err = rw.Close()
		require.NoError(t, err, "Close should not error with nil close function")
	})
}
