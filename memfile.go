package fs

import (
	"io"
	"io/ioutil"

	"github.com/ungerik/go-fs/fsimpl"
)

// MemFile implements FileReader with an in memory byte slice
type MemFile struct {
	name string
	data []byte
}

// NewMemFile returns a new MemFile
func NewMemFile(name string, data []byte) *MemFile {
	return &MemFile{name: name, data: data}
}

// NewMemFileReadAll returns a new MemFile with the data from ioutil.ReadAll(reader)
func NewMemFileReadAll(name string, reader io.Reader) (*MemFile, error) {
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return &MemFile{name: name, data: data}, nil
}

// Name returns the name of the file
func (f *MemFile) Name() string {
	return f.name
}

// Size returns the size of the file
func (f *MemFile) Size() int64 {
	return int64(len(f.data))
}

// ContentHash returns a Dropbox compatible content hash for the file.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ContentHash() (string, error) {
	return fsimpl.ContentHashBytes(f.data), nil
}

// ReadAll reads and returns all bytes of the file
func (f *MemFile) ReadAll() (data []byte, err error) {
	return data, nil
}

// ReadAllContentHash reads and returns all bytes of the file
// together with a Dropbox compatible content hash.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ReadAllContentHash() (data []byte, hash string, err error) {
	return data, fsimpl.ContentHashBytes(f.data), nil
}

// ReadAllString reads the complete file and returns the content as string.
func (f *MemFile) ReadAllString() (string, error) {
	return string(f.data), nil
}

// OpenReader opens the file and returns a io.ReadCloser that has be closed after reading
func (f *MemFile) OpenReader() (io.ReadCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.data), nil
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// Use OpenReader if seeking is not necessary because implementations
// may need additional buffering to support seeking or not support it at all.
func (f *MemFile) OpenReadSeeker() (ReadSeekCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.data), nil
}
