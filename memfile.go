package fs

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// Ensure MemFile implements interfaces
var (
	_ FileReader  = MemFile{}
	_ io.Writer   = &MemFile{}
	_ io.WriterTo = MemFile{}
	_ io.ReaderAt = MemFile{}
	// _ json.Marshaler   = MemFile{}
	// _ json.Unmarshaler = &MemFile{}
	_ gob.GobEncoder = MemFile{}
	_ gob.GobDecoder = &MemFile{}
	_ fmt.Stringer   = MemFile{}
)

// MemFile implements FileReader with a filename and an in memory byte slice.
// It exposes FileName and FileData as exported struct fields to emphasize
// its simple nature as just an wrapper around a name and some bytes.
//
// As a small an simple struct MemFile is usually passed by value.
// This is why NewMemFile does not return a pointer.
//
// Note that the ReadAll and ReadAllContext methods return FileData
// directly without copying it to optimized performance.
// So be careful when modifying the FileData bytes of a MemFile.
//
// MemFile implements the following interfaces:
//   - FileReader
//   - io.Writer
//   - io.WriterTo
//   - io.ReaderAt
//   - json.Marshaler
//   - json.Unmarshaler
//   - gob.GobEncoder
//   - gob.GobDecoder
//   - fmt.Stringer
type MemFile struct {
	FileName string `json:"filename"`
	FileData []byte `json:"data,omitempty"`
}

// NewMemFile returns a new MemFile
func NewMemFile(name string, data []byte) MemFile {
	return MemFile{FileName: name, FileData: data}
}

// ReadMemFile returns a new MemFile with name and data from fileReader.
// If the passed fileReader is a MemFile then
// its FileData is used directly without copying it.
func ReadMemFile(ctx context.Context, fileReader FileReader) (MemFile, error) {
	data, err := fileReader.ReadAllContext(ctx) // Does not copy in case of fileReader.(MemFile)
	if err != nil {
		return MemFile{}, fmt.Errorf("ReadMemFile: error reading from FileReader: %w", err)
	}
	return MemFile{FileName: fileReader.Name(), FileData: data}, nil
}

// ReadMemFileRename returns a new MemFile with the data from fileReader and the passed name.
// If the passed fileReader is a MemFile then
// its FileData is used directly without copying it.
func ReadMemFileRename(ctx context.Context, fileReader FileReader, name string) (MemFile, error) {
	data, err := fileReader.ReadAllContext(ctx) // Does not copy in case of fileReader.(MemFile)
	if err != nil {
		return MemFile{}, fmt.Errorf("ReadMemFileRename: error reading from FileReader: %w", err)
	}
	return MemFile{FileName: name, FileData: data}, nil
}

// ReadAllMemFile returns a new MemFile with the data
// from ReadAllContext(r) and the passed name.
// It reads all data from r until EOF is reached,
// another error is returned, or the context got canceled.
func ReadAllMemFile(ctx context.Context, r io.Reader, name string) (MemFile, error) {
	data, err := ReadAllContext(ctx, r)
	if err != nil {
		return MemFile{}, fmt.Errorf("ReadAllMemFile: error reading from io.Reader: %w", err)
	}
	return MemFile{FileName: name, FileData: data}, nil
}

// MemFilesAsFileReaders converts []MemFile to []FileReader
func MemFilesAsFileReaders(memFiles []MemFile) []FileReader {
	if len(memFiles) == 0 {
		return nil
	}
	fileReaders := make([]FileReader, len(memFiles))
	for i, memFile := range memFiles {
		fileReaders[i] = memFile
	}
	return fileReaders
}

// String returns the name and meta information for the FileReader.
// String implements the fmt.Stringer interface.
func (f MemFile) String() string {
	return fmt.Sprintf("MemFile{name: `%s`, size: %d}", f.FileName, len(f.FileData))
}

// Name returns the name of the file.
// If FileName contains a slash or backslash
// then only the part after it will be returned.
func (f MemFile) Name() string {
	if i := strings.LastIndexAny(f.FileName, `/\`); i >= 0 {
		return f.FileName[i+1:]
	}
	return f.FileName
}

// Ext returns the extension of file name including the point, or an empty string.
func (f MemFile) Ext() string {
	return fsimpl.Ext(f.FileName, "")
}

// LocalPath always returns an empty string for a MemFile.
func (MemFile) LocalPath() string {
	return ""
}

// Size returns the size of the file
func (f MemFile) Size() int64 {
	return int64(len(f.FileData))
}

// Exists returns true if the MemFile has non empty FileName.
// It's valid to call this method on a nil pointer,
// will return false in this case.
func (f MemFile) Exists() bool {
	return &f != nil && f.FileName != ""
}

// CheckExists return an ErrDoesNotExist error
// if the file does not exist.
func (f MemFile) CheckExists() error {
	if !f.Exists() {
		return NewErrDoesNotExistFileReader(f)
	}
	return nil
}

// ContentHash returns the DefaultContentHash for the file.
func (f MemFile) ContentHash() (string, error) {
	return f.ContentHashContext(context.Background())
}

// ContentHashContext returns the DefaultContentHash for the file.
func (f MemFile) ContentHashContext(ctx context.Context) (string, error) {
	return DefaultContentHash(ctx, bytes.NewReader(f.FileData))
}

// ReadAll returns the FileData without copying it.
func (f MemFile) ReadAll() (data []byte, err error) {
	return f.FileData, nil
}

// ReadAllContext returns the FileData without copying it.
func (f MemFile) ReadAllContext(ctx context.Context) (data []byte, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return f.FileData, nil
}

// ReadAllContentHash returns the FileData without copying it
// together with the DefaultContentHash.
func (f MemFile) ReadAllContentHash(ctx context.Context) (data []byte, hash string, err error) {
	hash, err = DefaultContentHash(ctx, bytes.NewReader(f.FileData))
	if err != nil {
		return nil, "", err
	}
	return f.FileData, hash, nil
}

// ReadAllString returns the FileData as string.
func (f MemFile) ReadAllString() (string, error) {
	return string(f.FileData), nil
}

// ReadAllStringContext returns the FileData as string.
func (f MemFile) ReadAllStringContext(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
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
func (f MemFile) ReadAt(p []byte, off int64) (n int, err error) {
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
func (f MemFile) WriteTo(writer io.Writer) (n int64, err error) {
	i, err := writer.Write(f.FileData)
	return int64(i), err
}

// OpenReader opens the file and returns a io/fs.File that has to be closed after reading
func (f MemFile) OpenReader() (ReadCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.FileData, memFileInfo{f}), nil
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// Use OpenReader if seeking is not necessary because implementations
// may need additional buffering to support seeking or not support it at all.
func (f MemFile) OpenReadSeeker() (ReadSeekCloser, error) {
	return fsimpl.NewReadonlyFileBuffer(f.FileData, memFileInfo{f}), nil
}

// ReadJSON reads and unmarshalles the JSON content of the file to output.
func (f MemFile) ReadJSON(ctx context.Context, output any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return json.Unmarshal(f.FileData, output)
}

// ReadXML reads and unmarshalles the XML content of the file to output.
func (f MemFile) ReadXML(ctx context.Context, output any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return xml.Unmarshal(f.FileData, output)
}

// // MarshalJSON implements the json.Marshaler interface
// func (f MemFile) MarshalJSON() ([]byte, error) {
// 	encodedData := base64.RawURLEncoding.EncodeToString(f.FileData)
// 	return json.Marshal(map[string]string{f.FileName: encodedData})
// }

// // UnmarshalJSON implements the json.Unmarshaler interface
// func (f *MemFile) UnmarshalJSON(j []byte) error {
// 	m := make(map[string]string, 1)
// 	err := json.Unmarshal(j, &m)
// 	if err != nil {
// 		return fmt.Errorf("can't unmarshal JSON as MemFile: %w", err)
// 	}
// 	if len(m) != 1 {
// 		return fmt.Errorf("can't unmarshal JSON as MemFile: %d object keys", len(m))
// 	}
// 	for fileName, encodedData := range m {
// 		fileData, err := base64.RawURLEncoding.DecodeString(encodedData)
// 		if err != nil {
// 			return fmt.Errorf("can't decode base64 JSON data of MemFile: %w", err)
// 		}
// 		f.FileName = fileName
// 		f.FileData = fileData
// 	}
// 	return nil
// }

// GobEncode gob encodes the file name and content,
// implementing encoding/gob.GobEncoder.
func (f MemFile) GobEncode() ([]byte, error) {
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

// Write appends the passed bytes to the FileData,
// implementing the io.Writer interface.
func (f *MemFile) Write(b []byte) (int, error) {
	f.FileData = append(f.FileData, b...)
	return len(b), nil
}

// Stat returns a io/fs.FileInfo describing the MemFile.
func (f MemFile) Stat() (iofs.FileInfo, error) {
	return memFileInfo{f}, nil
}

var _ iofs.FileInfo = memFileInfo{}

// memFileInfo implements io/fs.FileInfo for a MemFile.
//
// Name() is derived from MemFile.
type memFileInfo struct {
	MemFile
}

func (i memFileInfo) Mode() iofs.FileMode { return 0666 }
func (i memFileInfo) ModTime() time.Time  { return time.Now() }
func (i memFileInfo) IsDir() bool         { return false }
func (i memFileInfo) Sys() any            { return nil }
