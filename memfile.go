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
	"mime/multipart"
	"net/http"
	"path"
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
// The FileName can mirror the complete path of a file or directory on a
// file system, but the most common simple case is just a name without any
// slashes and path semantics. When the FileName contains slashes, the path
// related methods like Name, Dir, DirAndName, Ext and CleanPath interpret
// it as a '/' separated path. A FileName ending with a slash marks the
// MemFile as a directory (see IsDir), in which case FileData is ignored.
//
// Note that the ReadAll and ReadAllContext methods return FileData
// directly without copying it to optimize performance.
// So be careful when modifying the FileData bytes of a MemFile.
// Since MemFile implements FileReader, any function accepting a FileReader
// parameter may call these methods and receive a reference to the internal
// data without copying. This includes functions like ReadMemFile and
// ReadMemFileRename which will share the FileData when the input is a MemFile.
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

// NewMemFile returns a new MemFile by value (not a pointer).
// MemFile should be passed by value because it's a small, simple struct
// containing only a string and a slice (both reference types internally).
// Passing by value is more efficient and idiomatic for such lightweight types.
//
// The passed data slice is used directly without copying it,
// so be careful when modifying the data after passing it to this function.
func NewMemFile(name string, data []byte) MemFile {
	return MemFile{FileName: name, FileData: data}
}

// NewMemFileWriteJSON returns a new MemFile by value with the input mashalled to JSON as FileData.
// Any indent arguments will be concanated and used as JSON line indentation.
//
// Returns a wrapped ErrMarshalJSON when the marshalling failed.
func NewMemFileWriteJSON(name string, input any, indent ...string) (MemFile, error) {
	var (
		data []byte
		err  error
	)
	if len(indent) == 0 {
		data, err = json.Marshal(input)
	} else {
		data, err = json.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return MemFile{}, fmt.Errorf("%w because: %w", ErrMarshalJSON, err)
	}
	return MemFile{FileName: name, FileData: data}, nil
}

// NewMemFileWriteXML returns a new MemFile by value with the input mashalled to XML as FileData.
// Any indent arguments will be concanated and used as XML line indentation.
//
// Returns a wrapped ErrMarshalXML when the marshalling failed.
func NewMemFileWriteXML(name string, input any, indent ...string) (MemFile, error) {
	var (
		data []byte
		err  error
	)
	if len(indent) == 0 {
		data, err = xml.Marshal(input)
	} else {
		data, err = xml.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return MemFile{}, fmt.Errorf("%w because: %w", ErrMarshalXML, err)
	}
	return MemFile{FileName: name, FileData: append([]byte(xml.Header), data...)}, nil
}

// ReadMemFile returns a new MemFile by value with name and data from fileReader.
// If the passed fileReader is a MemFile then
// its FileData is used directly without copying it.
func ReadMemFile(ctx context.Context, fileReader FileReader) (MemFile, error) {
	data, err := fileReader.ReadAllContext(ctx) // Does not copy in case of fileReader.(MemFile)
	if err != nil {
		return MemFile{}, fmt.Errorf("ReadMemFile: error reading from FileReader: %w", err)
	}
	return MemFile{FileName: fileReader.Name(), FileData: data}, nil
}

// ReadMemFileRename returns a new MemFile by value with the data from fileReader and the passed name.
// If the passed fileReader is a MemFile then
// its FileData is used directly without copying it.
func ReadMemFileRename(ctx context.Context, fileReader FileReader, name string) (MemFile, error) {
	data, err := fileReader.ReadAllContext(ctx) // Does not copy in case of fileReader.(MemFile)
	if err != nil {
		return MemFile{}, fmt.Errorf("ReadMemFileRename: error reading from FileReader: %w", err)
	}
	return MemFile{FileName: name, FileData: data}, nil
}

// ReadAllMemFile returns a new MemFile by value with the data
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

// String returns the metadata of the file formatted as a string.
// String implements the fmt.Stringer interface.
func (f MemFile) String() string {
	return fmt.Sprintf("MemFile{name: `%s`, size: %d}", f.FileName, len(f.FileData))
}

// PrettyString implements the pretty.Stringer interface
// to provide a compact representation of the MemFile
// in error messages and pretty-printed output.
func (f MemFile) PrettyString() string {
	return f.String()
}

// Name returns the name of the file or directory, which is the last
// element of the FileName path. A single trailing slash marks a
// directory (see IsDir) and is ignored, so the name before it is returned.
func (f MemFile) Name() string {
	name := f.FileName
	// Ignore a single trailing slash that marks a directory.
	if n := len(name); n > 0 && name[n-1] == '/' {
		name = name[:n-1]
	}
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// Dir returns the parent directory of the FileName as a MemFile
// with nil FileData and the FileName truncated up to and including
// the last slash. Because the returned FileName ends with a slash
// (or is empty) the resulting MemFile is considered a directory.
//
// If FileName contains no slash then an empty MemFile is returned.
func (f MemFile) Dir() MemFile {
	i := strings.LastIndexByte(f.FileName, '/')
	if i < 0 {
		return MemFile{}
	}
	return MemFile{FileName: f.FileName[:i+1]}
}

// DirAndName returns the parent directory of the FileName as a MemFile
// together with the name of the last path element.
// It is equivalent to calling Dir and Name, see those methods for details,
// but computes both results in a single scan.
func (f MemFile) DirAndName() (dir MemFile, name string) {
	s := f.FileName
	if n := len(s); n > 0 && s[n-1] == '/' {
		// A single trailing slash marks a directory: the path is its own
		// parent directory, the name is the element before the trailing slash.
		dir = MemFile{FileName: s}
		s = s[:n-1]
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			return dir, s[i+1:]
		}
		return dir, s
	}
	// No trailing slash: a single scan splits the parent directory and name.
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return MemFile{FileName: s[:i+1]}, s[i+1:]
	}
	return MemFile{}, s
}

// CleanPath returns the FileName with "." and ".." path elements
// resolved, redundant slashes removed and trailing slashes trimmed,
// using '/' as separator. An empty FileName returns an empty string.
func (f MemFile) CleanPath() string {
	return cleanPath(f.FileName)
}

// WithName returns a MemFile with the passed name as its complete
// FileName and the same shared data as the original MemFile.
// Use WithData to derive a MemFile with the same name but different data.
//
// Note that name replaces the whole FileName including any path,
// so WithName is not symmetric with the Name method which only
// returns the last path element. To change just the name while
// keeping the directory, prepend the FileName of the Dir method:
//
//	f = f.WithName(f.Dir().FileName + newName)
func (f MemFile) WithName(name string) MemFile {
	return MemFile{FileName: name, FileData: f.FileData}
}

// WithData returns a MemFile with the passed data
// and the same name as the original MemFile.
// Use WithName to derive a MemFile with the same data but a different name.
//
// The passed data slice is used directly without copying it,
// so be careful when modifying the data after passing it to this function.
func (f MemFile) WithData(data []byte) MemFile {
	return MemFile{FileName: f.FileName, FileData: data}
}

// Ext returns the extension of the file or directory name including
// the point, or an empty string. '/' is used as separator and a
// trailing slash is ignored.
func (f MemFile) Ext() string {
	return path.Ext(f.Name())
}

// ExtLower returns the lower case extension of the FileName including the point, or an empty string.
func (f MemFile) ExtLower() string {
	return strings.ToLower(f.Ext())
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
	return f.FileName != ""
}

// CheckExists return an ErrDoesNotExist error
// if the file does not exist.
func (f MemFile) CheckExists() error {
	if !f.Exists() {
		return NewErrDoesNotExistFileReader(f)
	}
	return nil
}

// IsDir returns true if the FileName ends with a slash.
// In that case the MemFile is considered a directory and FileData is ignored.
func (f MemFile) IsDir() bool {
	return strings.HasSuffix(f.FileName, "/")
}

// CheckIsDir returns nil if the MemFile is a directory
// (its FileName ends with a slash), ErrEmptyPath if the FileName is empty,
// or an ErrIsNotDirectory error otherwise.
func (f MemFile) CheckIsDir() error {
	if f.FileName == "" {
		return ErrEmptyPath
	}
	if !f.IsDir() {
		return NewErrIsNotDirectory(f)
	}
	return nil
}

// ContentHash returns the DefaultContentHash for the file,
// or an empty string if the MemFile is a directory.
func (f MemFile) ContentHash() (string, error) {
	return f.ContentHashContext(context.Background())
}

// ContentHashContext returns the DefaultContentHash for the file,
// or an empty string if the MemFile is a directory.
func (f MemFile) ContentHashContext(ctx context.Context) (string, error) {
	if f.IsDir() {
		return "", nil
	}
	return DefaultContentHash(ctx, bytes.NewReader(f.FileData))
}

// ReadAll returns the FileData without copying it,
// or an ErrIsDirectory error if the MemFile is a directory.
//
// Be careful when modifying the returned data as it shares
// the same underlying array with the MemFile's FileData.
func (f MemFile) ReadAll() (data []byte, err error) {
	if f.IsDir() {
		return nil, NewErrIsDirectory(f)
	}
	return f.FileData, nil
}

// ReadAllContext returns the FileData without copying it,
// or an ErrIsDirectory error if the MemFile is a directory.
//
// Be careful when modifying the returned data as it shares
// the same underlying array with the MemFile's FileData.
func (f MemFile) ReadAllContext(ctx context.Context) (data []byte, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f.IsDir() {
		return nil, NewErrIsDirectory(f)
	}
	return f.FileData, nil
}

// ReadAllContentHash returns the FileData without copying it
// together with the DefaultContentHash,
// or an ErrIsDirectory error if the MemFile is a directory.
//
// Be careful when modifying the returned data as it shares
// the same underlying array with the MemFile's FileData.
func (f MemFile) ReadAllContentHash(ctx context.Context) (data []byte, hash string, err error) {
	if f.IsDir() {
		return nil, "", NewErrIsDirectory(f)
	}
	hash, err = DefaultContentHash(ctx, bytes.NewReader(f.FileData))
	if err != nil {
		return nil, "", err
	}
	return f.FileData, hash, nil
}

// ReadAllString returns the FileData as string,
// or an ErrIsDirectory error if the MemFile is a directory.
func (f MemFile) ReadAllString() (string, error) {
	if f.IsDir() {
		return "", NewErrIsDirectory(f)
	}
	return string(f.FileData), nil
}

// ReadAllStringContext returns the FileData as string,
// or an ErrIsDirectory error if the MemFile is a directory.
func (f MemFile) ReadAllStringContext(ctx context.Context) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if f.IsDir() {
		return "", NewErrIsDirectory(f)
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
// It returns an ErrIsDirectory error if the MemFile is a directory.
func (f MemFile) ReadAt(p []byte, off int64) (n int, err error) {
	if f.IsDir() {
		return 0, NewErrIsDirectory(f)
	}
	if off >= int64(len(f.FileData)) {
		return 0, io.EOF
	}
	n = copy(p, f.FileData[off:])
	if n < len(p) {
		return n, fmt.Errorf("could only read %d of %d requested bytes", n, len(p))
	}
	return n, nil
}

// WriteTo implements the io.WriterTo interface.
// It returns an ErrIsDirectory error if the MemFile is a directory.
func (f MemFile) WriteTo(writer io.Writer) (n int64, err error) {
	if f.IsDir() {
		return 0, NewErrIsDirectory(f)
	}
	i, err := writer.Write(f.FileData)
	return int64(i), err
}

// OpenReader opens the file and returns a io/fs.File that has to be closed after reading,
// or an ErrIsDirectory error if the MemFile is a directory.
func (f MemFile) OpenReader() (ReadCloser, error) {
	if f.IsDir() {
		return nil, NewErrIsDirectory(f)
	}
	return fsimpl.NewReadonlyFileBuffer(f.FileData, memFileInfo{f}), nil
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser,
// or an ErrIsDirectory error if the MemFile is a directory.
// Use OpenReader if seeking is not necessary because implementations
// may need additional buffering to support seeking or not support it at all.
func (f MemFile) OpenReadSeeker() (ReadSeekCloser, error) {
	if f.IsDir() {
		return nil, NewErrIsDirectory(f)
	}
	return fsimpl.NewReadonlyFileBuffer(f.FileData, memFileInfo{f}), nil
}

// ReadJSON reads and unmarshalles the JSON content of the file to output.
//
// Returns an ErrIsDirectory error if the MemFile is a directory,
// or a wrapped ErrUnmarshalJSON when the unmarshalling failed.
func (f MemFile) ReadJSON(ctx context.Context, output any) error {
	// Context is passed for identical call signature as other types
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.IsDir() {
		return NewErrIsDirectory(f)
	}
	err := json.Unmarshal(f.FileData, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalJSON, err)
	}
	return nil
}

// WriteJSON mashalles input to JSON and writes it as the file.
// Any indent arguments will be concanated and used as JSON line indentation.
//
// Returns a wrapped ErrMarshalJSON when the marshalling failed.
func (f *MemFile) WriteJSON(ctx context.Context, input any, indent ...string) (err error) {
	// Context is passed for identical call signature as other types
	if err = ctx.Err(); err != nil {
		return err
	}
	var data []byte
	if len(indent) == 0 {
		data, err = json.Marshal(input)
	} else {
		data, err = json.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrMarshalJSON, err)
	}
	f.FileData = data
	return nil
}

// ReadXML reads and unmarshalles the XML content of the file to output.
//
// Returns an ErrIsDirectory error if the MemFile is a directory,
// or a wrapped ErrUnmarshalXML when the unmarshalling failed.
func (f MemFile) ReadXML(ctx context.Context, output any) error {
	// Context is passed for identical call signature as other types
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.IsDir() {
		return NewErrIsDirectory(f)
	}
	err := xml.Unmarshal(f.FileData, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalXML, err)
	}
	return nil
}

// WriteXML mashalles input to XML and writes it as the file.
// Any indent arguments will be concanated and used as XML line indentation.
//
// Returns a wrapped ErrMarshalXML when the marshalling failed.
func (f *MemFile) WriteXML(ctx context.Context, input any, indent ...string) (err error) {
	// Context is passed for identical call signature as other types
	if err = ctx.Err(); err != nil {
		return err
	}
	var data []byte
	if len(indent) == 0 {
		data, err = xml.Marshal(input)
	} else {
		data, err = xml.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrMarshalXML, err)
	}
	f.FileData = append([]byte(xml.Header), data...)
	return nil
}

// // MarshalJSON implements the json.Marshaler interface
// func (f MemFile) MarshalJSON() ([]byte, error) {
// 	encodedData := base64.RawURLEncoding.EncodeToString(f.FileData)
//  // fmt.Errorf("%w because: %w", ErrMarshalJSON, err)
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
// Name() and IsDir() are derived from the embedded MemFile.
type memFileInfo struct {
	MemFile
}

func (i memFileInfo) Mode() iofs.FileMode {
	if i.IsDir() {
		return iofs.ModeDir | 0777
	}
	return 0666
}
func (i memFileInfo) ModTime() time.Time { return time.Now() }
func (i memFileInfo) Sys() any           { return nil }

// ReadMultipartFormMemFiles reads the multipart form
// and returns a map of form field name to MemFiles.
func ReadMultipartFormMemFiles(ctx context.Context, form *multipart.Form) (map[string][]MemFile, error) {
	memFiles := make(map[string][]MemFile, len(form.File))
	for fieldName, files := range form.File {
		for _, file := range files {
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer reader.Close()
			memFile, err := ReadAllMemFile(ctx, reader, file.Filename)
			if err != nil {
				return nil, err
			}
			memFiles[fieldName] = append(memFiles[fieldName], memFile)
		}
	}
	return memFiles, nil
}

// ParseRequestMultipartFormMemFiles parses the multipart form from the request
// and returns a map of form field name to MemFiles.
func ParseRequestMultipartFormMemFiles(request *http.Request, maxMemory int64) (map[string][]MemFile, error) {
	err := request.ParseMultipartForm(maxMemory) //#nosec G120 -- caller bounds memory via maxMemory parameter
	if err != nil {
		return nil, err
	}
	return ReadMultipartFormMemFiles(request.Context(), request.MultipartForm)
}
