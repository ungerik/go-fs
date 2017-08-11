package fs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
)

const copyBufferSize = 1024 * 1024

// CopyFile copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
func CopyFile(src, dest File, perm ...Permissions) error {
	var buf []byte
	return CopyFileBuf(src, dest, &buf, perm...)
}

// CopyFileBuf copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// buf must point to a []byte variable.
// If that variable is initialized with a byte slice, then this slice will be used as buffer,
// else a byte slice will be allocated for the variable.
// Use this function to re-use buffers between CopyFileBuf calls.
func CopyFileBuf(src, dest File, buf *[]byte, perm ...Permissions) error {
	if buf == nil {
		panic("CopyFileBuf: buf is nil")
	}

	// Handle directories
	if dest.IsDir() {
		dest = dest.Relative(src.Name())
	} else {
		err := dest.Dir().MakeDir()
		if err != nil {
			return err
		}
	}

	// Use inner file system copy if possible
	fs := src.FileSystem()
	if fs == dest.FileSystem() {
		return fs.CopyFile(src.Path(), dest.Path(), buf)
	}

	r, err := src.OpenReader()
	if err != nil {
		return err
	}
	defer r.Close()

	if len(perm) == 0 {
		perm = []Permissions{src.Permissions()}
	}
	w, err := dest.OpenWriter(perm...)
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

// CopyRecursive can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursive(src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(src, dest, patterns, &buf)
}

func copyRecursive(src, dest File, patterns []string, buf *[]byte) error {
	if !src.IsDir() {
		// Just copy one file
		return CopyFileBuf(src, dest, buf)
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
		return copyRecursive(file, dest.Relative(file.Name()), patterns, buf)
	}, patterns...)
}

// MatchAnyPatternImpl returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via path.Match.
// FileSystem implementations can use this function to implement
// FileSystem.MatchAnyPattern they use "/" as path separator.
func MatchAnyPatternImpl(name string, patterns []string) (bool, error) {
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

// ListDirFunc is the ListDir function call pattern used by ListDirMaxImpl
type ListDirFunc func(callback func(File) error) error

// ListDirMaxImpl implements the ListDirMax method functionality
// by calling listDir.
// FileSystem implementations can use this function to implement ListDirMax,
// if a own, specialized implementation doesn't make sense.
func (listDir ListDirFunc) ListDirMaxImpl(max int) (files []File, err error) {
	errAbort := errors.New("") // used as an internal flag, won't be returned
	err = listDir(func(file File) error {
		if len(files) >= max {
			return errAbort
		}
		if files == nil {
			if max > 0 {
				files = make([]File, 0, max)
			} else {
				files = make([]File, 0, 32)
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil && err != errAbort {
		return nil, err
	}
	return files, nil
}

// ListDirRecursiveImpl can be used by FileSystem implementations to
// implement FileSystem.ListDirRecursive if it doesn't have an internal
// optimzed form of doing that.
func ListDirRecursiveImpl(fs FileSystem, dirPath string, callback func(File) error, patterns []string) error {
	return fs.ListDir(dirPath, func(f File) error {
		if f.IsDir() {
			err := f.ListDirRecursive(callback, patterns...)
			// Don't mind files that have been deleted while iterating
			if IsErrDoesNotExist(err) {
				err = nil
			}
			return err
		}
		match, err := fs.MatchAnyPattern(f.Name(), patterns)
		if match {
			err = callback(f)
		}
		return err
	}, nil)
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

const hashBlockSize = 4 * 1024 * 1024

// ContentHash returns a Dropbox compatible content hash for an io.Reader.
// See https://www.dropbox.com/developers/reference/content-hash
func ContentHash(r io.Reader) (string, error) {
	buf := make([]byte, hashBlockSize)
	resultHash := sha256.New()
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n > 0 {
		bufHash := sha256.Sum256(buf[:n])
		resultHash.Write(bufHash[:])
	}
	for n == hashBlockSize && err == nil {
		n, err = r.Read(buf)
		if err != nil && err != io.EOF {
			return "", err
		}
		if n > 0 {
			bufHash := sha256.Sum256(buf[:n])
			resultHash.Write(bufHash[:])
		}
	}
	return fmt.Sprintf("%x", resultHash.Sum(nil)), nil
}

// ContentHashBytes returns a Dropbox compatible content hash for a byte slice.
// See https://www.dropbox.com/developers/reference/content-hash
func ContentHashBytes(buf []byte) (string, error) {
	return ContentHash(bytes.NewBuffer(buf))
}

// FilesToURLs returns the URLs of a slice of Files.
func FilesToURLs(files []File) (fileURLs []string) {
	fileURLs = make([]string, len(files))
	for i := range files {
		fileURLs[i] = files[i].URL()
	}
	return fileURLs
}

// FilesToPaths returns the FileSystem specific paths of a slice of Files.
func FilesToPaths(files []File) (paths []string) {
	paths = make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Path()
	}
	return paths
}

// URIsToFiles returns Files for the given fileURIs.
func URIsToFiles(fileURIs []string) (files []File) {
	files = make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = FileFrom(fileURIs[i])
	}
	return files
}

// DirAndNameImpl is a generic helper for FileSystem.DirAndName implementations.
// path.Split or filepath.Split don't have the wanted behaviour when given a path ending in a separator.
func DirAndNameImpl(filePath string, volumeLen int, pathSeparator byte) (dir, name string) {
	if filePath == "" {
		return "", ""
	}
	// Ignore trailing separator
	last := len(filePath) - 1
	if filePath[last] == pathSeparator {
		last--
	}

	sep := last
	for sep >= volumeLen && filePath[sep] != pathSeparator {
		sep--
	}
	dir = filePath[:sep]
	name = filePath[sep+1 : last+1]
	// fmt.Printf("'%s' -> '%s', '%s'\n", filePath, dir, name)
	return dir, name
}
