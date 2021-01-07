package fs

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"

	"github.com/ungerik/go-fs/fsimpl"
)

// Check if MemFile implements FileReader
var _ FileReader = new(MemFile)

// MemFile implements FileReader with a filename and an in memory byte slice.
// It exposes FileName and FileData as exported struct fields to emphasize
// its simple nature as just an wrapper of a name and some bytes.
type MemFile struct {
	FileName string
	FileData []byte
}

// NewMemFile returns a new MemFile
func NewMemFile(name string, data []byte) *MemFile {
	return &MemFile{FileName: name, FileData: data}
}

// ReadMemFile returns a new MemFile with
// the name from fileReader.Name() and
// the data from fileReader.ReadAll()
func ReadMemFile(fileReader FileReader) (*MemFile, error) {
	data, err := fileReader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("ReadMemFile: error reading from FileReader: %w", err)
	}
	return &MemFile{FileName: fileReader.Name(), FileData: data}, nil
}

// ReadMemFileRename returns a new MemFile with the data
// from fileReader.ReadAll() and the passed name.
func ReadMemFileRename(fileReader FileReader, name string) (*MemFile, error) {
	data, err := fileReader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("ReadMemFileRename: error reading from FileReader: %w", err)
	}
	return &MemFile{FileName: name, FileData: data}, nil
}

// String returns the name and meta information for the FileReader.
// String implements the fmt.Stringer interface.
func (f *MemFile) String() string {
	return fmt.Sprintf("MemFile{name: %q, size: %d}", f.FileName, len(f.FileData))
}

// Name returns the name of the file
func (f *MemFile) Name() string {
	return f.FileName
}

// Ext returns the extension of file name including the point, or an empty string.
func (f *MemFile) Ext() string {
	return fsimpl.Ext(f.FileName, "")
}

// LocalPath always returns an empty string for a MemFile.
func (f *MemFile) LocalPath() string {
	return ""
}

// Size returns the size of the file
func (f *MemFile) Size() int64 {
	return int64(len(f.FileData))
}

// Exists returns true if the MemFile has non empty FileName.
// It's valid to call this method on a nil pointer,
// will return false in this case.
func (f *MemFile) Exists() bool {
	return f != nil && f.FileName != ""
}

// CheckExists return an ErrDoesNotExist error
// if the file does not exist.
func (f *MemFile) CheckExists() error {
	if !f.Exists() {
		return NewErrDoesNotExistFileReader(f)
	}
	return nil
}

// ContentHash returns a Dropbox compatible content hash for the file.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ContentHash() (string, error) {
	return fsimpl.ContentHashBytes(f.FileData), nil
}

// ReadAll copies all bytes of the file
func (f *MemFile) ReadAll() (data []byte, err error) {
	return append([]byte(nil), f.FileData...), nil
}

// ReadAllContentHash copies all bytes of the file
// together with a Dropbox compatible content hash.
// See https://www.dropbox.com/developers/reference/content-hash
func (f *MemFile) ReadAllContentHash() (data []byte, hash string, err error) {
	return append([]byte(nil), f.FileData...), fsimpl.ContentHashBytes(f.FileData), nil
}

// ReadAllString reads the complete file and returns the content as string.
func (f *MemFile) ReadAllString() (string, error) {
	return string(f.FileData), nil
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes
// read (0 <= n <= len(p)) and any error encountered.
//
// When ReadAt returns n < len(p), it returns a non-nil error
// explaining why more bytes were not returned. In this respect,
// ReadAt is stricter than Read.
//
// If the n = len(p) bytes returned by ReadAt are at the end of the
// input source, ReadAt returns err == nil.
//
// Clients of ReadAt can execute parallel ReadAt calls on the
// same input source.
//
// ReadAt implements the interface io.ReaderAt.
func (f *MemFile) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= int64(len(f.FileData)) {
		return 0, io.EOF
	}
	n = copy(p, f.FileData[off:])
	if n < len(p) {
		return n, fmt.Errorf("could only read %d of %d requested bytes", n, len(p))
	}
	return n, nil
}

// WriteTo implements the io.WriterTo interface
func (f *MemFile) WriteTo(writer io.Writer) (n int64, err error) {
	i, err := writer.Write(f.FileData)
	return int64(i), err
}

// OpenReader opens the file and returns a io.ReadCloser that has be closed after reading
func (f *MemFile) OpenReader() (io.ReadCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.FileData), nil
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// Use OpenReader if seeking is not necessary because implementations
// may need additional buffering to support seeking or not support it at all.
func (f *MemFile) OpenReadSeeker() (ReadSeekCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.FileData), nil
}

// ReadJSON reads and unmarshalles the JSON content of the file to output.
func (f *MemFile) ReadJSON(output interface{}) error {
	return json.Unmarshal(f.FileData, output)
}

// ReadXML reads and unmarshalles the XML content of the file to output.
func (f *MemFile) ReadXML(output interface{}) error {
	return xml.Unmarshal(f.FileData, output)
}

// GobEncode gob encodes the file name and content,
// implementing encoding/gob.GobEncoder.
func (f *MemFile) GobEncode() ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 16+len(f.FileName)+len(f.FileData)))
	enc := gob.NewEncoder(buf)
	err := enc.Encode(f.FileName)
	if err != nil {
		return nil, fmt.Errorf("MemFile.GobEncode: error encoding FileName: %w", err)
	}
	err = enc.Encode(f.FileData)
	if err != nil {
		return nil, fmt.Errorf("MemFile.GobEncode: error encoding FileData: %w", err)
	}
	return buf.Bytes(), nil
}

// GobDecode decodes gobBytes file name and content,
// implementing encoding/gob.GobDecoder.
func (f *MemFile) GobDecode(gobBytes []byte) error {
	dec := gob.NewDecoder(bytes.NewReader(gobBytes))
	err := dec.Decode(&f.FileName)
	if err != nil {
		return fmt.Errorf("MemFile.GobDecode: error decoding FileName: %w", err)
	}
	err = dec.Decode(&f.FileData)
	if err != nil {
		return fmt.Errorf("MemFile.GobDecode: error decoding FileData: %w", err)
	}
	return nil
}
