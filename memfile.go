package fs

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
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

// NewMemFileReadAll returns a new MemFile with the data from ioutil.ReadAll(ioReader)
func NewMemFileReadAll(name string, ioReader io.Reader) (*MemFile, error) {
	data, err := ioutil.ReadAll(ioReader)
	if err != nil {
		return nil, err
	}
	return &MemFile{name: name, data: data}, nil
}

// NewMemFileCopy returns a new MemFile with the data from fileReader.ReadAll()
func NewMemFileCopy(name string, fileReader FileReader) (*MemFile, error) {
	data, err := fileReader.ReadAll()
	if err != nil {
		return nil, err
	}
	return &MemFile{name: name, data: data}, nil
}

// String returns the name and meta information for the FileReader.
func (f *MemFile) String() string {
	return fmt.Sprintf("MemFile{name: %#v, size: %d}", f.name, len(f.data))
}

// Name returns the name of the file
func (f *MemFile) Name() string {
	return f.name
}

// Ext returns the extension of file name including the point, or an empty string.
func (f *MemFile) Ext() string {
	return fsimpl.Ext(f.name)
}

// Size returns the size of the file
func (f *MemFile) Size() int64 {
	return int64(len(f.data))
}

// Exists returns if the MemFile has a Name
func (f *MemFile) Exists() bool {
	return f.name != ""
}

// ContentHash returns a Dropbox compatible content hash for the file.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ContentHash() (string, error) {
	return fsimpl.ContentHashBytes(f.data), nil
}

// ReadAll reads and returns all bytes of the file
func (f *MemFile) ReadAll() (data []byte, err error) {
	return f.data, nil
}

// ReadAllContentHash reads and returns all bytes of the file
// together with a Dropbox compatible content hash.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ReadAllContentHash() (data []byte, hash string, err error) {
	return f.data, fsimpl.ContentHashBytes(f.data), nil
}

// ReadAllString reads the complete file and returns the content as string.
func (f *MemFile) ReadAllString() (string, error) {
	return string(f.data), nil
}

// WriteTo implements the io.WriterTo interface
func (f *MemFile) WriteTo(writer io.Writer) (n int64, err error) {
	i, err := writer.Write(f.data)
	return int64(i), err
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

// ReadJSON reads and unmarshalles the JSON content of the file to output.
func (f *MemFile) ReadJSON(output interface{}) error {
	return json.Unmarshal(f.data, output)
}

// ReadXML reads and unmarshalles the XML content of the file to output.
func (f *MemFile) ReadXML(output interface{}) error {
	return xml.Unmarshal(f.data, output)
}

func (f *MemFile) Data() []byte {
	return f.data
}
