package fs

import (
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
)

const copyBufferSize = 1024 * 1024

func copyFile(src, dest File, patterns []string, buf *[]byte) error {
	if !src.IsDir() {
		// Just copy one file
		if dest.IsDir() {
			dest = dest.Relative(src.Name())
		} else {
			err := dest.Dir().MakeDir()
			if err != nil {
				return err
			}
		}

		r, err := src.OpenReader()
		if err != nil {
			return err
		}
		defer r.Close()

		w, err := dest.OpenWriter(src.Permissions())
		if err != nil {
			return err
		}
		defer w.Close()

		if *buf == nil {
			*buf = make([]byte, copyBufferSize)
		}
		_, err = io.CopyBuffer(w, r, *buf)
		return err
	}

	if dest.Exists() && !dest.IsDir() {
		return fmt.Errorf("Can't copy a directory (%s) over a file (%s)", src.URL(), dest.URL())
	}

	// No error if dest is already a dir
	err := dest.MakeDir()
	if err != nil {
		return err
	}

	// Copy directories recursive
	return src.ListDir(func(file File) error {
		return copyFile(file, dest.Relative(file.Name()), patterns, buf)
	}, patterns...)
}

// Copy copies even between files of different file systems
func Copy(src, dest File, patterns ...string) error {
	var buf []byte
	return copyFile(src, dest, patterns, &buf)
}

// CopyPath copies even between files of different file systems
func CopyPath(src, dest string, patterns ...string) error {
	var buf []byte
	return copyFile(CleanPath(src), CleanPath(dest), patterns, &buf)
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via path.Match
func MatchAnyPattern(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := path.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

// MatchAnyPatternLocal returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via filepath.Match
func MatchAnyPatternLocal(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := filepath.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

// ListDirMaxImpl implements the ListDirMax method functionality
// by calling fs.ListDir.
// FileSystem implementations can use this function to implement ListDirMax,
// if a own, specialized implementation doesn't make sense.
func ListDirMaxImpl(fs FileSystem, filePath string, n int, patterns []string) (files []File, err error) {
	if n <= 0 {
		files = make([]File, 0)
	} else {
		files = make([]File, 0, n)
	}
	err = fs.ListDir(filePath, func(file File) error {
		if len(files) >= n {
			return ErrAbortListDir
		}
		files = append(files, file)
		return nil
	}, patterns)
	if err != nil && err != ErrAbortListDir {
		return nil, err
	}
	return files, nil
}

// ReadonlyFileBuffer is a memory buffer that implements ReadSeekCloser which combines the interfaces
// io.Reader
// io.ReaderAt
// io.Seeker
// io.Closer
type ReadonlyFileBuffer struct {
	data  []byte
	pos   int64
	close func() error
}

// NewReadonlyFileBuffer returns a new ReadonlyFileBuffer
func NewReadonlyFileBuffer(data []byte) *ReadonlyFileBuffer {
	return &ReadonlyFileBuffer{data: data}
}

// NewReadonlyFileBufferWithClose returns a new ReadonlyFileBuffer
func NewReadonlyFileBufferWithClose(data []byte, close func() error) *ReadonlyFileBuffer {
	return &ReadonlyFileBuffer{data: data, close: close}
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
	n, err = buf.ReadAt(p, buf.pos)
	buf.pos += int64(n)
	return n, err
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered.
func (buf *ReadonlyFileBuffer) ReadAt(p []byte, off int64) (n int, err error) {
	pos := int(off)
	if pos >= len(buf.data) {
		return 0, io.ErrShortBuffer
	}
	return copy(p, buf.data[pos:]), nil
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
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = buf.pos + offset
	case io.SeekEnd:
		newPos = buf.Size() + offset
	default:
		return buf.pos, errors.New("Seek: invalid whence")
	}
	if newPos < 0 {
		return buf.pos, errors.New("Seek: negative position")
	}
	buf.pos = newPos
	return newPos, nil
}

// Close is a no-op
func (buf *ReadonlyFileBuffer) Close() error {
	if buf.close == nil {
		return nil
	}
	return buf.close()
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
func (buf *ReadonlyFileBuffer) Write(p []byte) (n int, err error) {
	n, err = buf.WriteAt(p, buf.pos)
	buf.pos += int64(n)
	return n, err
}

// WriteAt writes len(p) bytes from p to the underlying data stream
// at offset off. It returns the number of bytes written from p (0 <= n <= len(p))
// and any error encountered that caused the write to stop early.
// WriteAt must return a non-nil error if it returns n < len(p).
func (buf *ReadonlyFileBuffer) WriteAt(p []byte, off int64) (n int, err error) {
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
