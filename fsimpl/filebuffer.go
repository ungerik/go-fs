package fsimpl

import (
	"errors"
	"io"
	iofs "io/fs"
	"math"
	"os"
	"slices"
)

var _ iofs.File = new(ReadOnlyFileBuffer)

// ReadOnlyFileBuffer is a memory buffer that implements ReadAtSeekCloser which combines the interfaces
//
//	io/fs.File
//	io.Reader
//	io.ReaderAt
//	io.Seeker
//	io.Closer
//
// After Close every method except Stat, Bytes and Size returns os.ErrClosed.
type ReadOnlyFileBuffer struct {
	info   iofs.FileInfo
	data   []byte
	pos    int64        // current reading index
	commit func() error // called by Close, see NewWriteOnCloseFileBuffer
	closed bool
}

// NewReadOnlyFileBuffer returns a new ReadOnlyFileBuffer
func NewReadOnlyFileBuffer(data []byte, info iofs.FileInfo) *ReadOnlyFileBuffer {
	return &ReadOnlyFileBuffer{data: data, info: info}
}

// NewReadOnlyFileBufferReadAll reads all data from reader and returns it
// wrapped as a ReadOnlyFileBuffer with the passed info,
// or an error if reading from reader failed.
func NewReadOnlyFileBufferReadAll(reader io.Reader, info iofs.FileInfo) (*ReadOnlyFileBuffer, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return NewReadOnlyFileBuffer(data, info), nil
}

// Stat returns the iofs.FileInfo of the buffer,
// implementing the io/fs.File interface.
// An error is returned if the buffer was created without a FileInfo.
func (buf *ReadOnlyFileBuffer) Stat() (iofs.FileInfo, error) {
	if buf.info == nil {
		return nil, errors.New("ReadOnlyFileBuffer.Stat: no FileInfo available")
	}
	return buf.info, nil
}

// Bytes returns the bytes of the buffer.
func (buf *ReadOnlyFileBuffer) Bytes() []byte {
	return buf.data
}

// Size returns the size of buffered file in bytes.
func (buf *ReadOnlyFileBuffer) Size() int64 {
	return int64(len(buf.data))
}

// Read reads up to len(p) bytes into p. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered. Even if Read
// returns n < len(p), it may use all of p as scratch space during the call.
// If some data is available but not len(p) bytes, Read conventionally
// returns what is available instead of waiting for more.
func (buf *ReadOnlyFileBuffer) Read(p []byte) (n int, err error) {
	if buf.closed {
		return 0, os.ErrClosed
	}
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
func (buf *ReadOnlyFileBuffer) ReadAt(p []byte, off int64) (n int, err error) {
	// cannot modify state - see io.ReaderAt
	if buf.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, errors.New("ReadOnlyFileBuffer.ReadAt: negative offset")
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
func (buf *ReadOnlyFileBuffer) Seek(offset int64, whence int) (newPos int64, err error) {
	if buf.closed {
		return 0, os.ErrClosed
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = buf.pos + offset
	case io.SeekEnd:
		abs = int64(len(buf.data)) + offset
	default:
		return 0, errors.New("ReadOnlyFileBuffer.Seek: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("ReadOnlyFileBuffer.Seek: negative position")
	}
	buf.pos = abs
	return abs, nil
}

// Close frees the internal buffer. Close is idempotent,
// all methods except Stat, Bytes and Size return os.ErrClosed afterwards.
func (buf *ReadOnlyFileBuffer) Close() (err error) {
	if buf.closed {
		return nil
	}
	if buf.commit != nil {
		err = buf.commit()
	}
	buf.release()
	return err
}

// release frees the internal buffer and marks it as closed
// without calling the commit function.
func (buf *ReadOnlyFileBuffer) release() {
	buf.data = nil
	buf.pos = 0
	buf.commit = nil
	buf.closed = true
}

// FileBuffer is a memory buffer that implements ReadWriteSeekCloser which combines the interfaces
// io.Reader
// io.ReaderAt
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
//
// It also has the Abort method of fs.WriteAborter, see NewWriteOnCloseFileBuffer.
// After Close or Abort every method except Stat, Bytes and Size returns os.ErrClosed,
// so data written to a closed buffer is never silently dropped.
type FileBuffer struct {
	ReadOnlyFileBuffer
}

// NewFileBuffer returns a new FileBuffer
func NewFileBuffer(data []byte) *FileBuffer {
	return &FileBuffer{ReadOnlyFileBuffer: ReadOnlyFileBuffer{data: data}}
}

// NewWriteOnCloseFileBuffer returns a new FileBuffer initialized with data
// that passes its complete content to the write function when it is closed.
// This is the building block for file systems without random access writes
// (object stores, FTP, ZIP archives) that have to upload whole files.
//
// Abort discards the buffered data without calling write,
// so an upload that failed midway doesn't replace the file.
func NewWriteOnCloseFileBuffer(data []byte, write func(data []byte) error) *FileBuffer {
	buf := NewFileBuffer(data)
	buf.commit = func() error { return write(buf.data) }
	return buf
}

// Abort frees the internal buffer without passing it to the write function
// of NewWriteOnCloseFileBuffer, implementing fs.WriteAborter.
// Close and Abort after Close or Abort are no-ops that return nil.
func (buf *FileBuffer) Abort() error {
	buf.release()
	return nil
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
	if buf.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, errors.New("FileBuffer.WriteAt: negative offset")
	}
	// int(off) would truncate on 32 bit and pos+len(p) would wrap
	// negative near the maximum, slicing below would then panic
	// instead of returning an error
	if off > math.MaxInt-int64(len(p)) {
		return 0, errors.New("FileBuffer.WriteAt: offset out of range")
	}
	pos := int(off)
	writeEnd := pos + len(p)
	if oldLen := len(buf.data); writeEnd > oldLen {
		// Grow with amortized reallocation, streaming writes
		// would otherwise copy the whole buffer on every call
		buf.data = slices.Grow(buf.data, writeEnd-oldLen)[:writeEnd]
		if pos > oldLen {
			clear(buf.data[oldLen:pos]) // spare capacity may hold stale data
		}
	}
	n = copy(buf.data[pos:], p)
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

// Truncate changes the size of the buffered data.
// If the buffer is larger than size, the extra data is lost.
// If the buffer is smaller, it is extended with zeros.
func (buf *FileBuffer) Truncate(size int64) error {
	if buf.closed {
		return os.ErrClosed
	}
	if size < 0 {
		return errors.New("FileBuffer.Truncate: negative size")
	}
	switch {
	case size < int64(len(buf.data)):
		buf.data = buf.data[:size]
	case size > int64(len(buf.data)):
		buf.data = append(buf.data, make([]byte, size-int64(len(buf.data)))...)
	}
	if buf.pos > size {
		buf.pos = size
	}
	return nil
}
