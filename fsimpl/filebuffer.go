package fsimpl

import (
	"errors"
	"io"
	iofs "io/fs"
	"math"
	"slices"
)

var _ iofs.File = new(ReadonlyFileBuffer)

// ReadonlyFileBuffer is a memory buffer that implements ReadSeekCloser which combines the interfaces
//
//	io/fs.File
//	io.Reader
//	io.ReaderAt
//	io.Seeker
//	io.Closer
type ReadonlyFileBuffer struct {
	info  iofs.FileInfo
	data  []byte
	pos   int64 // current reading index
	close func() error
}

// NewReadonlyFileBuffer returns a new ReadonlyFileBuffer
func NewReadonlyFileBuffer(data []byte, info iofs.FileInfo) *ReadonlyFileBuffer {
	return &ReadonlyFileBuffer{data: data, info: info}
}

// NewReadonlyFileBufferReadAll reads all data from reader and returns it
// wrapped as a ReadonlyFileBuffer with the passed info,
// or an error if reading from reader failed.
func NewReadonlyFileBufferReadAll(reader io.Reader, info iofs.FileInfo) (*ReadonlyFileBuffer, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return NewReadonlyFileBuffer(data, info), nil
}

// Stat returns the iofs.FileInfo of the buffer,
// implementing the io/fs.File interface.
// An error is returned if the buffer was created without a FileInfo.
func (buf *ReadonlyFileBuffer) Stat() (iofs.FileInfo, error) {
	if buf.info == nil {
		return nil, errors.New("ReadonlyFileBuffer.Stat: no FileInfo available")
	}
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
// that calls the passed close function when its Close method is called.
func NewFileBufferWithClose(data []byte, close func() error) *FileBuffer {
	return &FileBuffer{ReadonlyFileBuffer: ReadonlyFileBuffer{data: data, close: close}}
}

// NewWriteOnCloseFileBuffer returns a new FileBuffer initialized with data
// that passes its complete content to the write function when it is closed.
// This is the building block for file systems without random access writes
// (object stores, FTP, ZIP archives) that have to upload whole files.
func NewWriteOnCloseFileBuffer(data []byte, write func(data []byte) error) *FileBuffer {
	buf := NewFileBuffer(data)
	buf.close = func() error { return write(buf.data) }
	return buf
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
