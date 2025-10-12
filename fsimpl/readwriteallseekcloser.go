package fsimpl

import "io"

var _ io.ReadWriteSeeker = (*ReadWriteAllSeekCloser)(nil)

// ReadWriteAllSeekCloser implements io.ReadWriteSeeker and io.Closer by lazily reading
// all data from a file when first needed, buffering modifications in memory, and writing
// everything back to the file on Close().
//
// This is useful for file systems that don't support true random access writes,
// such as ZIP archives, where files must be completely rewritten.
//
// The implementation:
//  1. Lazily reads all file content into memory on first read/write operation
//  2. Performs all read/write/seek operations in memory using a FileBuffer
//  3. Writes the complete modified content back to the file on Close()
type ReadWriteAllSeekCloser struct {
	readAll  func() ([]byte, error)
	writeAll func([]byte) error
	buffer   *FileBuffer
	modified bool
}

// NewReadWriteAllSeekCloser creates a new ReadWriteAllSeekCloser.
// The file content is not read until the first read or write operation.
// close is optional and will be called on Close() if not nil.
func NewReadWriteAllSeekCloser(readAll func() ([]byte, error), writeAll func([]byte) error) *ReadWriteAllSeekCloser {
	return &ReadWriteAllSeekCloser{
		readAll:  readAll,
		writeAll: writeAll,
		buffer:   nil,
		modified: false,
	}
}

// ensureBuffer ensures the buffer is initialized by reading from the file if needed.
func (rw *ReadWriteAllSeekCloser) ensureBuffer() error {
	if rw.buffer != nil {
		return nil
	}

	data, err := rw.readAll()
	if err != nil {
		return err
	}

	rw.buffer = NewFileBuffer(data)
	return nil
}

// InvalidateBuffer discards the buffered data and marks the buffer as uninitialized.
// The next read or write operation will reload the data from the file.
// This is useful when the underlying file has been modified externally.
func (rw *ReadWriteAllSeekCloser) InvalidateBuffer() {
	rw.buffer = nil
	rw.modified = false
}

// Read reads up to len(p) bytes into p from the buffered file content.
func (rw *ReadWriteAllSeekCloser) Read(p []byte) (n int, err error) {
	if err := rw.ensureBuffer(); err != nil {
		return 0, err
	}
	return rw.buffer.Read(p)
}

// ReadAt reads len(p) bytes into p starting at offset off in the buffered file content.
func (rw *ReadWriteAllSeekCloser) ReadAt(p []byte, off int64) (n int, err error) {
	if err := rw.ensureBuffer(); err != nil {
		return 0, err
	}
	return rw.buffer.ReadAt(p, off)
}

// Write writes len(p) bytes from p to the buffered file content.
// The data is not written to the actual file until Close() is called.
func (rw *ReadWriteAllSeekCloser) Write(p []byte) (n int, err error) {
	if err := rw.ensureBuffer(); err != nil {
		return 0, err
	}
	rw.modified = true
	return rw.buffer.Write(p)
}

// WriteAt writes len(p) bytes from p to the buffered file content at offset off.
// The data is not written to the actual file until Close() is called.
func (rw *ReadWriteAllSeekCloser) WriteAt(p []byte, off int64) (n int, err error) {
	if err := rw.ensureBuffer(); err != nil {
		return 0, err
	}
	rw.modified = true
	return rw.buffer.WriteAt(p, off)
}

// Seek sets the offset for the next Read or Write operation.
func (rw *ReadWriteAllSeekCloser) Seek(offset int64, whence int) (int64, error) {
	if err := rw.ensureBuffer(); err != nil {
		return 0, err
	}
	return rw.buffer.Seek(offset, whence)
}

// Close writes all buffered modifications back to the file if any writes occurred,
// then closes the underlying file.
func (rw *ReadWriteAllSeekCloser) Close() error {
	if !rw.modified {
		return nil
	}

	// Write back if modified
	return rw.writeAll(rw.buffer.Bytes())
}
