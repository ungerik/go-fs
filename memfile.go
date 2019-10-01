package fs

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

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

// NewMemFileFromReader returns a new MemFile with the data from ioutil.ReadAll(ioReader)
func NewMemFileFromReader(name string, ioReader io.Reader) (*MemFile, error) {
	data, err := ioutil.ReadAll(ioReader)
	if err != nil {
		return nil, err
	}
	return &MemFile{FileName: name, FileData: data}, nil
}

// NewMemFileFromFileReader returns a new MemFile with the data from fileReader.ReadAll()
func NewMemFileFromFileReader(name string, fileReader FileReader) (*MemFile, error) {
	data, err := fileReader.ReadAll()
	if err != nil {
		return nil, err
	}
	return &MemFile{FileName: name, FileData: data}, nil
}

// NewMemFileFrom returns a new MemFile with
// the name from fileReader.Name() and
// the data from fileReader.ReadAll()
func NewMemFileFrom(fileReader FileReader) (*MemFile, error) {
	data, err := fileReader.ReadAll()
	if err != nil {
		return nil, err
	}
	return &MemFile{FileName: fileReader.Name(), FileData: data}, nil
}

// NewMemFileFromMarshalJSON returns a new MemFile with
// the result of marshalling input as JSON with optional indentation.
// (indent strings will be joined)
func NewMemFileFromMarshalJSON(name string, input interface{}, indent ...string) (*MemFile, error) {
	var (
		data []byte
		err  error
	)
	if len(indent) > 0 {
		data, err = json.MarshalIndent(input, "", strings.Join(indent, ""))
	} else {
		data, err = json.Marshal(input)
	}
	if err != nil {
		return nil, err
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
	return fsimpl.Ext(f.FileName)
}

// LocalPath always returns an empty string for a MemFile.
func (f *MemFile) LocalPath() string {
	return ""
}

// Size returns the size of the file
func (f *MemFile) Size() int64 {
	return int64(len(f.FileData))
}

// Exists returns if the MemFile has a Name
func (f *MemFile) Exists() bool {
	return f != nil && f.FileName != ""
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
		return nil, err
	}
	err = enc.Encode(f.FileData)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GobDecode decodes gobBytes file name and content,
// implementing encoding/gob.GobDecoder.
func (f *MemFile) GobDecode(gobBytes []byte) error {
	dec := gob.NewDecoder(bytes.NewReader(gobBytes))
	err := dec.Decode(&f.FileName)
	if err != nil {
		return err
	}
	return dec.Decode(&f.FileData)
}
