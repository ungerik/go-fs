package fs

import "io"

// ReadSeekCloser combines the interfaces
// io.Reader
// io.ReaderAt
// io.Seeker
// io.Closer
type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

// WriteSeekCloser combines the interfaces
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
type WriteSeekCloser interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

// ReadWriteSeekCloser combines the interfaces
// io.Reader
// io.ReaderAt
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
type ReadWriteSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}
