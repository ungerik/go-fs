package fs

import "io"

// ReadAtSeekCloser combines the interfaces
// io.Reader
// io.ReaderAt
// io.Seeker
// io.Closer
//
// It is returned by FileReader.OpenReadSeeker.
// Unlike io.ReadSeekCloser it also supports io.ReaderAt.
type ReadAtSeekCloser interface {
	io.Reader
	io.ReaderAt
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
