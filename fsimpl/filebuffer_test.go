package fsimpl

import (
	"crypto/rand"
	"io"
	iofs "io/fs"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileBuffer(t *testing.T) {
	t.Run("BasicReadWriteOperations", func(t *testing.T) {
		data := []byte("Hello, FileBuffer!")
		buf := NewFileBuffer(data)
		defer buf.Close()

		// Test initial read
		readBuf := make([]byte, 5)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("Hello"), readBuf)

		// Test Write (writes at current position after "Hello")
		n, err = buf.Write([]byte(" World"))
		require.NoError(t, err)
		assert.Equal(t, 6, n)

		// Seek back to start and read
		newPos, err := buf.Seek(0, io.SeekStart)
		require.NoError(t, err)
		assert.Equal(t, int64(0), newPos)

		result := make([]byte, 18)
		n, err = buf.Read(result)
		require.NoError(t, err)
		assert.Equal(t, 18, n)
		// "Hello" + " World" overwrites ", File" (positions 5-10)
		// Result: "Hello WorldBuffer!"
		assert.Equal(t, []byte("Hello WorldBuffer!"), result)
	})

	t.Run("WriteExpandsBuffer", func(t *testing.T) {
		buf := NewFileBuffer([]byte("Small"))
		defer buf.Close()

		// Write beyond current buffer size
		_, err := buf.Seek(10, io.SeekStart)
		require.NoError(t, err)

		n, err := buf.Write([]byte("Extended"))
		require.NoError(t, err)
		assert.Equal(t, 8, n)
		assert.Equal(t, 18, len(buf.Bytes()))

		// Verify content
		buf.Seek(0, io.SeekStart)
		result := make([]byte, 18)
		buf.Read(result)
		assert.Equal(t, []byte("Small\x00\x00\x00\x00\x00Extended"), result)
	})

	t.Run("WriteAt", func(t *testing.T) {
		buf := NewFileBuffer([]byte("0123456789"))
		defer buf.Close()

		// Write at specific offset
		n, err := buf.WriteAt([]byte("XYZ"), 3)
		require.NoError(t, err)
		assert.Equal(t, 3, n)

		// Read entire buffer
		buf.Seek(0, io.SeekStart)
		result := make([]byte, 10)
		buf.Read(result)
		assert.Equal(t, []byte("012XYZ6789"), result)
	})

	t.Run("WriteAtExpandsBuffer", func(t *testing.T) {
		buf := NewFileBuffer([]byte("Short"))
		defer buf.Close()

		// Write beyond current buffer size
		n, err := buf.WriteAt([]byte("Extended"), 10)
		require.NoError(t, err)
		assert.Equal(t, 8, n)
		assert.Equal(t, 18, len(buf.Bytes()))

		// Verify content
		expected := []byte("Short\x00\x00\x00\x00\x00Extended")
		assert.Equal(t, expected, buf.Bytes())
	})

	t.Run("ReadAndWrite", func(t *testing.T) {
		buf := NewFileBuffer([]byte("Hello, World!"))
		defer buf.Close()

		// Read first 5 bytes
		readBuf := make([]byte, 5)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("Hello"), readBuf)

		// Write at current position (overwrite ", Wor")
		n, err = buf.Write([]byte("123"))
		require.NoError(t, err)
		assert.Equal(t, 3, n)

		// Read rest
		rest := make([]byte, 5)
		n, err = buf.Read(rest)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("orld!"), rest)

		// Verify entire buffer
		buf.Seek(0, io.SeekStart)
		all := make([]byte, 13)
		buf.Read(all)
		assert.Equal(t, []byte("Hello123orld!"), all)
	})

	t.Run("SeekOperations", func(t *testing.T) {
		buf := NewFileBuffer([]byte("0123456789"))
		defer buf.Close()

		// SeekStart
		newPos, err := buf.Seek(5, io.SeekStart)
		require.NoError(t, err)
		assert.Equal(t, int64(5), newPos)

		readBuf := make([]byte, 2)
		buf.Read(readBuf)
		assert.Equal(t, []byte("56"), readBuf)

		// SeekCurrent backward
		newPos, err = buf.Seek(-3, io.SeekCurrent)
		require.NoError(t, err)
		assert.Equal(t, int64(4), newPos)

		buf.Read(readBuf)
		assert.Equal(t, []byte("45"), readBuf)

		// SeekEnd
		newPos, err = buf.Seek(-2, io.SeekEnd)
		require.NoError(t, err)
		assert.Equal(t, int64(8), newPos)

		buf.Read(readBuf)
		assert.Equal(t, []byte("89"), readBuf)
	})

	t.Run("CloseWithCallback", func(t *testing.T) {
		closeCalled := false
		closeFunc := func() error {
			closeCalled = true
			return nil
		}

		buf := NewFileBufferWithClose([]byte("Test"), closeFunc)

		err := buf.Close()
		require.NoError(t, err)
		assert.True(t, closeCalled)
		assert.Nil(t, buf.data)
	})

	t.Run("EmptyBuffer", func(t *testing.T) {
		buf := NewFileBuffer([]byte{})
		defer buf.Close()

		// Read should return EOF immediately
		readBuf := make([]byte, 10)
		n, err := buf.Read(readBuf)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)

		// Write should work
		n, err = buf.Write([]byte("New"))
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, 3, len(buf.Bytes()))
	})

	t.Run("NilBuffer", func(t *testing.T) {
		buf := NewFileBuffer(nil)
		defer buf.Close()

		// Write should create buffer
		n, err := buf.Write([]byte("Created"))
		require.NoError(t, err)
		assert.Equal(t, 7, n)
		assert.Equal(t, []byte("Created"), buf.Bytes())
	})

	t.Run("WriteAtZeroPosition", func(t *testing.T) {
		buf := NewFileBuffer([]byte("0123456789"))
		defer buf.Close()

		// Write at position 0
		n, err := buf.WriteAt([]byte("ABC"), 0)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("ABC3456789"), buf.Bytes())
	})

	t.Run("MultipleWrites", func(t *testing.T) {
		buf := NewFileBuffer([]byte{})
		defer buf.Close()

		// Multiple sequential writes
		buf.Write([]byte("Hello"))
		buf.Write([]byte(" "))
		buf.Write([]byte("World"))
		buf.Write([]byte("!"))

		assert.Equal(t, []byte("Hello World!"), buf.Bytes())
	})

	t.Run("ReadAfterWrite", func(t *testing.T) {
		buf := NewFileBuffer([]byte("Initial"))
		defer buf.Close()

		// Write and then read back
		buf.Seek(0, io.SeekEnd)
		buf.Write([]byte(" Data"))

		buf.Seek(0, io.SeekStart)
		result := make([]byte, 12)
		n, err := buf.Read(result)
		require.NoError(t, err)
		assert.Equal(t, 12, n)
		assert.Equal(t, []byte("Initial Data"), result)
	})

	t.Run("Bytes", func(t *testing.T) {
		data := []byte("Test Data")
		buf := NewFileBuffer(data)
		defer buf.Close()

		// Bytes should return the internal buffer
		bytes := buf.Bytes()
		assert.Equal(t, data, bytes)

		// Seek to end and write to append
		buf.Seek(0, io.SeekEnd)
		buf.Write([]byte(" More"))
		bytes = buf.Bytes()
		assert.Equal(t, []byte("Test Data More"), bytes)
	})

	t.Run("Size", func(t *testing.T) {
		buf := NewFileBuffer([]byte("0123456789"))
		defer buf.Close()

		assert.Equal(t, int64(10), buf.Size())

		// Writing beyond size should increase Size
		buf.WriteAt([]byte("Extended"), 15)
		assert.Equal(t, int64(23), buf.Size())
	})

	t.Run("IOTestReader", func(t *testing.T) {
		randBytes1000 := make([]byte, 1000)
		_, err := rand.Read(randBytes1000)
		require.NoError(t, err)

		testData := [][]byte{
			nil,
			{},
			{0},
			{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
			randBytes1000,
		}
		for _, data := range testData {
			err = iotest.TestReader(NewFileBuffer(data), data)
			require.NoError(t, err)
		}
	})

	t.Run("InterfaceCompliance", func(t *testing.T) {
		buf := NewFileBuffer([]byte("Test"))
		defer buf.Close()

		// Verify it implements the expected interfaces
		var _ io.Reader = buf
		var _ io.ReaderAt = buf
		var _ io.Writer = buf
		var _ io.WriterAt = buf
		var _ io.Seeker = buf
		var _ io.Closer = buf
	})
}

// mockReadWriteAllFile implements ReadWriteAllCloser for testing
type mockReadWriteAllFile struct {
	data      []byte
	readCount int
}

func (m *mockReadWriteAllFile) ReadAll() ([]byte, error) {
	m.readCount++
	result := make([]byte, len(m.data))
	copy(result, m.data)
	return result, nil
}

func (m *mockReadWriteAllFile) WriteAll(data []byte) error {
	m.data = make([]byte, len(data))
	copy(m.data, data)
	return nil
}

func TestReadonlyFileBuffer(t *testing.T) {
	t.Run("BasicReadOperations", func(t *testing.T) {
		data := []byte("Hello, ReadonlyFileBuffer!")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Test Size
		assert.Equal(t, int64(len(data)), buf.Size())

		// Test Bytes
		assert.Equal(t, data, buf.Bytes())

		// Test Read
		readBuf := make([]byte, 5)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("Hello"), readBuf)

		// Read more
		n, err = buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte(", Rea"), readBuf)
	})

	t.Run("ReadEOF", func(t *testing.T) {
		data := []byte("Short")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Read all data
		readBuf := make([]byte, 10)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("Short"), readBuf[:n])

		// Try to read again, should get EOF
		n, err = buf.Read(readBuf)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("ReadAt", func(t *testing.T) {
		data := []byte("0123456789")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Read at specific offset
		readBuf := make([]byte, 3)
		n, err := buf.ReadAt(readBuf, 5)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("567"), readBuf)

		// ReadAt should not modify the internal position
		n, err = buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("012"), readBuf)
	})

	t.Run("ReadAtEOF", func(t *testing.T) {
		data := []byte("Short")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Try to read past end
		readBuf := make([]byte, 10)
		n, err := buf.ReadAt(readBuf, 3)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, []byte("rt"), readBuf[:n])

		// Try to read at end
		n, err = buf.ReadAt(readBuf, 5)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)

		// Try to read beyond end
		n, err = buf.ReadAt(readBuf, 10)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("ReadAtNegativeOffset", func(t *testing.T) {
		data := []byte("Test")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		readBuf := make([]byte, 2)
		n, err := buf.ReadAt(readBuf, -1)
		assert.Error(t, err)
		assert.Equal(t, 0, n)
		assert.Contains(t, err.Error(), "negative offset")
	})

	t.Run("SeekStart", func(t *testing.T) {
		data := []byte("0123456789")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Seek to position 5
		newPos, err := buf.Seek(5, io.SeekStart)
		require.NoError(t, err)
		assert.Equal(t, int64(5), newPos)

		// Read should start from position 5
		readBuf := make([]byte, 3)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("567"), readBuf)
	})

	t.Run("SeekCurrent", func(t *testing.T) {
		data := []byte("0123456789")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Read some data first
		readBuf := make([]byte, 3)
		buf.Read(readBuf)

		// Seek forward 2 positions from current
		newPos, err := buf.Seek(2, io.SeekCurrent)
		require.NoError(t, err)
		assert.Equal(t, int64(5), newPos)

		// Read should start from position 5
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("567"), readBuf)

		// Seek backward from current
		newPos, err = buf.Seek(-3, io.SeekCurrent)
		require.NoError(t, err)
		assert.Equal(t, int64(5), newPos)
	})

	t.Run("SeekEnd", func(t *testing.T) {
		data := []byte("0123456789")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Seek to 3 bytes before end
		newPos, err := buf.Seek(-3, io.SeekEnd)
		require.NoError(t, err)
		assert.Equal(t, int64(7), newPos)

		// Read should get last 3 bytes
		readBuf := make([]byte, 3)
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, 3, n)
		assert.Equal(t, []byte("789"), readBuf)

		// Seek to end
		newPos, err = buf.Seek(0, io.SeekEnd)
		require.NoError(t, err)
		assert.Equal(t, int64(10), newPos)

		// Read should return EOF
		n, err = buf.Read(readBuf)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("SeekNegativePosition", func(t *testing.T) {
		data := []byte("Test")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Try to seek before start
		newPos, err := buf.Seek(-1, io.SeekStart)
		assert.Error(t, err)
		assert.Equal(t, int64(0), newPos)
		assert.Contains(t, err.Error(), "negative position")

		// Try to seek before start with SeekEnd
		newPos, err = buf.Seek(-10, io.SeekEnd)
		assert.Error(t, err)
		assert.Equal(t, int64(0), newPos)
		assert.Contains(t, err.Error(), "negative position")
	})

	t.Run("SeekInvalidWhence", func(t *testing.T) {
		data := []byte("Test")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		newPos, err := buf.Seek(0, 999)
		assert.Error(t, err)
		assert.Equal(t, int64(0), newPos)
		assert.Contains(t, err.Error(), "invalid whence")
	})

	t.Run("Close", func(t *testing.T) {
		data := []byte("Test data")
		buf := NewReadonlyFileBuffer(data, nil)

		// Verify data is accessible before close
		assert.NotNil(t, buf.data)
		assert.Equal(t, int64(9), buf.Size())

		// Close the buffer
		err := buf.Close()
		require.NoError(t, err)

		// Verify data is freed
		assert.Nil(t, buf.data)
		assert.Equal(t, int64(0), buf.pos)
	})

	t.Run("CloseWithCallback", func(t *testing.T) {
		closeCalled := false
		closeFunc := func() error {
			closeCalled = true
			return nil
		}

		data := []byte("Test data")
		buf := NewReadonlyFileBufferWithClose(data, nil, closeFunc)

		// Close should call the callback
		err := buf.Close()
		require.NoError(t, err)
		assert.True(t, closeCalled)
	})

	t.Run("Stat", func(t *testing.T) {
		type mockFileInfo struct {
			iofs.FileInfo
			name string
			size int64
		}

		info := &mockFileInfo{name: "test.txt", size: 100}
		data := []byte("Test data")
		buf := NewReadonlyFileBuffer(data, info)
		defer buf.Close()

		// Test Stat
		statInfo, err := buf.Stat()
		require.NoError(t, err)
		assert.Equal(t, info, statInfo)
	})

	t.Run("NewReadonlyFileBufferReadAll", func(t *testing.T) {
		data := []byte("Data from reader")
		reader := strings.NewReader(string(data))

		buf, err := NewReadonlyFileBufferReadAll(reader, nil)
		require.NoError(t, err)
		defer buf.Close()

		assert.Equal(t, data, buf.Bytes())
		assert.Equal(t, int64(len(data)), buf.Size())

		// Read and verify
		readBuf := make([]byte, len(data))
		n, err := buf.Read(readBuf)
		require.NoError(t, err)
		assert.Equal(t, len(data), n)
		assert.Equal(t, data, readBuf)
	})

	t.Run("EmptyBuffer", func(t *testing.T) {
		buf := NewReadonlyFileBuffer([]byte{}, nil)
		defer buf.Close()

		assert.Equal(t, int64(0), buf.Size())
		assert.Equal(t, []byte{}, buf.Bytes())

		// Read should return EOF immediately
		readBuf := make([]byte, 10)
		n, err := buf.Read(readBuf)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("NilBuffer", func(t *testing.T) {
		buf := NewReadonlyFileBuffer(nil, nil)
		defer buf.Close()

		assert.Equal(t, int64(0), buf.Size())
		assert.Nil(t, buf.Bytes())

		// Read should return EOF immediately
		readBuf := make([]byte, 10)
		n, err := buf.Read(readBuf)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("InterfaceCompliance", func(t *testing.T) {
		data := []byte("Interface test")
		buf := NewReadonlyFileBuffer(data, nil)
		defer buf.Close()

		// Verify it implements the expected interfaces
		var _ iofs.File = buf
		var _ io.Reader = buf
		var _ io.ReaderAt = buf
		var _ io.Seeker = buf
		var _ io.Closer = buf
	})
}
