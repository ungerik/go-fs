package fsimpl

import (
	"fmt"
	"io"
	"os"
)

// RangeReader implements io.ReadSeekCloser and io.ReaderAt
// for remote files that are read with byte range requests,
// like HTTP GET with a Range header or object store downloads.
//
// Sequential reads stream the body opened for the current position,
// a Seek to another position closes the body so the next Read
// opens a new one from there. ReadAt opens a body for exactly
// the requested range independent of the sequential position.
type RangeReader struct {
	// Size of the file in bytes.
	Size int64

	// Open returns the file content starting at offset,
	// limited to count bytes if count is not negative.
	// The returned body must be positioned at offset even
	// if the server ignored the requested range.
	Open func(offset, count int64) (io.ReadCloser, error)

	pos    int64
	body   io.ReadCloser // nil before the first Read and after Seek
	closed bool
}

func (r *RangeReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.pos >= r.Size {
		return 0, io.EOF
	}
	if r.body == nil {
		body, err := r.Open(r.pos, -1)
		if err != nil {
			return 0, err
		}
		r.body = body
	}
	n, err := r.body.Read(p)
	r.pos += int64(n)
	if n > 0 && err == io.EOF {
		err = nil // report EOF with the next Read like bufio does
	}
	return n, err
}

func (r *RangeReader) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = r.pos + offset
	case io.SeekEnd:
		pos = r.Size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if pos < 0 {
		return 0, fmt.Errorf("negative seek position: %d", pos)
	}
	if pos != r.pos && r.body != nil {
		_ = r.body.Close()
		r.body = nil
	}
	r.pos = pos
	return pos, nil
}

// ReadAt reads len(p) bytes at offset off with a single range request,
// independent of the position of the sequential reads.
func (r *RangeReader) ReadAt(p []byte, off int64) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, fmt.Errorf("negative offset: %d", off)
	}
	if off >= r.Size {
		return 0, io.EOF
	}
	// Zero bytes requested were zero bytes delivered, like bytes.Reader
	if len(p) == 0 {
		return 0, nil
	}
	count := min(int64(len(p)), r.Size-off)
	body, err := r.Open(off, count)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	n, err := io.ReadFull(body, p[:count])
	if err == io.ErrUnexpectedEOF || (err == nil && int64(n) < int64(len(p))) {
		err = io.EOF
	}
	return n, err
}

// Close closes the body of the sequential reads if one is open.
// Close is idempotent, reads after Close return os.ErrClosed.
func (r *RangeReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.body != nil {
		return r.body.Close()
	}
	return nil
}
