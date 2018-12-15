// fsimpl contains helper functions for implementing a fs.FileSystem
package fsimpl

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/ioutil"
	"net/url"
	"path"
	"strings"
)

// RandomString returns a 120 bit randum number
// encoded as URL compatible base64 string with a length of 20 characters.
func RandomString() string {
	var buffer [15]byte
	b := buffer[:]
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Ext returns the extension of filePath including the point, or an empty string.
// Example: Ext("image.png") == ".png"
func Ext(filePath string) string {
	p := strings.LastIndexByte(filePath, '.')
	if p == -1 {
		return ""
	}
	return filePath[p:]
}

// TrimExt returns a filePath with a path where the extension is removed.
func TrimExt(filePath string) string {
	p := strings.LastIndexByte(filePath, '.')
	if p == -1 {
		return filePath
	}
	return filePath[:p]
}

// DirAndName is a generic helper for FileSystem.DirAndName implementations.
// path.Split or filepath.Split don't have the wanted behaviour when given a path ending in a separator.
// DirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
// If filePath is the root of the file systeme, then an empty string will be returned as name.
// If filePath does not contain a separator before the name part, then "." will be returned as dir.
func DirAndName(filePath string, volumeLen int, separator string) (dir, name string) {
	if filePath == "" {
		return "", ""
	}

	filePath = strings.TrimSuffix(filePath, separator)

	if filePath == "" {
		return separator, ""
	}

	pos := strings.LastIndex(filePath, separator)
	if pos == -1 {
		return ".", filePath
	} else if pos <= volumeLen {
		return filePath, ""
	}

	return filePath[:pos], filePath[pos+1:]
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via path.Match.
// FileSystem implementations can use this function to implement
// FileSystem.MatchAnyPattern they use "/" as path separator.
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

func NewReadonlyFileBufferReadAll(reader io.Reader) (*ReadonlyFileBuffer, error) {
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return NewReadonlyFileBuffer(data), nil
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

// DropboxContentHash returns a Dropbox compatible content hash by reading from an io.Reader until io.EOF.
// See https://www.dropbox.com/developers/reference/content-hash
func DropboxContentHash(reader io.Reader) (string, error) {
	buf := make([]byte, hashBlockSize)
	resultHash := sha256.New()
	n, err := reader.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n > 0 {
		bufHash := sha256.Sum256(buf[:n])
		resultHash.Write(bufHash[:])
	}
	for n == hashBlockSize && err == nil {
		n, err = reader.Read(buf)
		if err != nil && err != io.EOF {
			return "", err
		}
		if n > 0 {
			bufHash := sha256.Sum256(buf[:n])
			resultHash.Write(bufHash[:])
		}
	}
	return hex.EncodeToString(resultHash.Sum(nil)), nil
}

type ContentHasher interface {
	Hash(reader io.Reader) (string, error)
}

type ContentHasherFunc func(reader io.Reader) (string, error)

func (f ContentHasherFunc) Hash(reader io.Reader) (string, error) {
	return f(reader)
}

var ContentHash = ContentHasherFunc(DropboxContentHash)

// ContentHashBytes returns a Dropbox compatible content hash for a byte slice.
// See https://www.dropbox.com/developers/reference/content-hash
func ContentHashBytes(buf []byte) string {
	// bytes.Reader.Read only ever returns io.EOF
	// which is not treatet as error by ContentHash
	// so we can ignore all returned errors
	hash, _ := ContentHash(bytes.NewReader(buf))
	return hash
}

func JoinCleanPath(uriParts []string, prefix, separator string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], prefix)
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	if !strings.HasPrefix(cleanPath, separator) {
		cleanPath = separator + cleanPath
	}
	return path.Clean(cleanPath)
}

func SplitPath(filePath, prefix, separator string) []string {
	filePath = strings.TrimPrefix(filePath, prefix)
	filePath = strings.TrimPrefix(filePath, separator)
	filePath = strings.TrimSuffix(filePath, separator)
	return strings.Split(filePath, separator)
}
