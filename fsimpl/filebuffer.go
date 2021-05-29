package fsimpl

import (
	"errors"
	"io"
	"io/fs"
)

var _ fs.File = new(ReadonlyFileBuffer)

// ReadonlyFileBuffer is a memory buffer that implements ReadSeekCloser which combines the interfaces
//   io/fs.File
//   io.Reader
//   io.ReaderAt
//   io.Seeker
//   io.Closer
type ReadonlyFileBuffer struct {
	info  fs.FileInfo
	data  []byte
	pos   int64 // current reading index
	close func() error
}

// NewReadonlyFileBuffer returns a new ReadonlyFileBuffer
func NewReadonlyFileBuffer(data []byte, info fs.FileInfo) *ReadonlyFileBuffer {
	return &ReadonlyFileBuffer{data: data, info: info}
}

func NewReadonlyFileBufferReadAll(reader io.Reader, info fs.FileInfo) (*ReadonlyFileBuffer, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return NewReadonlyFileBuffer(data, info), nil
}

// NewReadonlyFileBufferWithClose returns a new ReadonlyFileBuffer
func NewReadonlyFileBufferWithClose(data []byte, info fs.FileInfo, close func() error) *ReadonlyFileBuffer {
	return &ReadonlyFileBuffer{data: data, info: info, close: close}
}

func (buf *ReadonlyFileBuffer) Stat() (fs.FileInfo, error) {
	return buf.info, nil
}

// Bytes returns the bytes of the buffer.
func (buf *ReadonlyFileBuffer) Bytes() []byte {
	return buf.data
}

// Size returns the size of buffered file in bytes.
func (buf *ReadonlyFileBuffer) Size() int64 {
	return int64(len(buf.data))
}

// Read reads up to len(p) bytes into p. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered. Even if Read
// returns n < len(p), it may use all of p as scratch space during the call.
// If some data is available but not len(p) bytes, Read conventionally
// returns what is available instead of waiting for more.
func (buf *ReadonlyFileBuffer) Read(p []byte) (n int, err error) {
	if buf.pos >= int64(len(buf.data)) {
		return 0, io.EOF
	}
	n = copy(p, buf.data[buf.pos:])
	buf.pos += int64(n)
	return
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered.
func (buf *ReadonlyFileBuffer) ReadAt(p []byte, off int64) (n int, err error) {
	// cannot modify state - see io.ReaderAt
	if off < 0 {
		return 0, errors.New("ReadonlyFileBuffer.ReadAt: negative offset")
	}
	if off >= int64(len(buf.data)) {
		return 0, io.EOF
	}
	n = copy(p, buf.data[off:])
	if n < len(p) {
		err = io.EOF
	}
	return
}

// Seek sets the offset for the next Read or Write to offset,
// interpreted according to whence:
// SeekStart means relative to the start of the file,
// SeekCurrent means relative to the current offset, and
// SeekEnd means relative to the end.
// Seek returns the new offset relative to the start of the
// file and an error, if any.
//
// Seeking to an offset before the start of the file is an error.
// Seeking to any positive offset is legal, but the behavior of subsequent
// I/O operations on the underlying object is implementation-dependent.
func (buf *ReadonlyFileBuffer) Seek(offset int64, whence int) (newPos int64, err error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = buf.pos + offset
	case io.SeekEnd:
		abs = int64(len(buf.data)) + offset
	default:
		return 0, errors.New("ReadonlyFileBuffer.Seek: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("ReadonlyFileBuffer.Seek: negative position")
	}
	buf.pos = abs
	return abs, nil
}

// Close will free the internal buffer
func (buf *ReadonlyFileBuffer) Close() (err error) {
	if buf.close != nil {
		err = buf.close()
	}
	buf.data = nil
	buf.pos = 0
	buf.close = nil
	return err
}

// FileBuffer is a memory buffer that implements ReadWriteSeekCloser which combines the interfaces
// io.Reader
// io.ReaderAt
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
type FileBuffer struct {
	ReadonlyFileBuffer
}

// NewFileBuffer returns a new FileBuffer
func NewFileBuffer(data []byte) *FileBuffer {
	return &FileBuffer{ReadonlyFileBuffer: ReadonlyFileBuffer{data: data}}
}

// NewFileBufferWithClose returns a new FileBuffer
func NewFileBufferWithClose(data []byte, close func() error) *FileBuffer {
	return &FileBuffer{ReadonlyFileBuffer: ReadonlyFileBuffer{data: data, close: close}}
}

// Write writes len(p) bytes from p to the underlying data stream.
// It returns the number of bytes written from p (0 <= n <= len(p))
// and any error encountered that caused the write to stop early.
func (buf *FileBuffer) Write(p []byte) (n int, err error) {
	n, err = buf.WriteAt(p, buf.pos)
	buf.pos += int64(n)
	return n, err
}

// WriteAt writes len(p) bytes from p to the underlying data stream
// at offset off. It returns the number of bytes written from p (0 <= n <= len(p))
// and any error encountered that caused the write to stop early.
// WriteAt must return a non-nil error if it returns n < len(p).
func (buf *FileBuffer) WriteAt(p []byte, off int64) (n int, err error) {
	numBytes := len(p)
	pos := int(buf.pos)
	writeEnd := pos + numBytes
	if writeEnd > len(buf.data) {
		newData := make([]byte, writeEnd)
		copy(newData, buf.data)
		buf.data = newData
	}
	return copy(buf.data[pos:], p), nil
}
