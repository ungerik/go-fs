// Package multipartfs implements a read-only file system
// for the files of a parsed multipart HTTP form.
//
// NewFromRequest parses the form of a request and New wraps an already
// parsed form. After the file system was registered with fs.Register,
// the uploaded files are accessible as fs.File values, see FormFile
// and FormFiles, and the form values via FormValue, FormValues and
// the Form field. Close unregisters the file system and removes the
// temporary files of the form:
//
//	formFS, err := multipartfs.NewFromRequest(request, 32<<20, "")
//	if err != nil {
//		return err
//	}
//	err = fs.Register(formFS)
//	if err != nil {
//		return err
//	}
//	defer formFS.Close()
package multipartfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix for the MultipartFileSystem
	Prefix = "multipart://"

	// Separator used in MultipartFileSystem paths
	Separator = "/"

	// unnamedFile is the file system name of an uploaded file
	// whose name is not usable as a path element.
	unnamedFile = "unnamed"
)

var (
	// Make sure MultipartFileSystem implements fs.FileSystem
	_ fs.FileSystem        = new(MultipartFileSystem)
	_ fs.ExistsFileSystem  = new(MultipartFileSystem)
	_ fs.ReadAllFileSystem = new(MultipartFileSystem)
)

// MultipartFileSystem wraps the files in a MIME multipart message as fs.FileSystem.
//
// The form fields with uploaded files are the directories of the file system
// and the files uploaded under a field name are the files in that directory,
// so the file system has exactly two levels and a path has the form
// "/<form field>/<file name>".
//
// Because a file system path has to identify exactly one file, the names of
// the uploaded files are made unique per directory: the second file with an
// already used name gets a " (2)" suffix before its extension, the third a
// " (3)" and so on. Uploaded names that are not usable as a path element
// (like "." and "..") are replaced by "unnamed". All other file names are
// used as they are, including names that look URL escaped like "a%20b.pdf".
//
// Form field names are not restricted by mime/multipart, so the directory
// name of a field is escaped: "%" becomes "%25", "/" becomes "%2F", and the
// names "." and ".." become "%2E" and "%2E%2E". Other field names, like
// "files" or "my files", are used as they are. A path is split into its
// elements before the field name is unescaped, so neither a field nor a file
// name can address an upload of another field.
//
// The uploaded files are read-only with the permissions fs.AllRead
// and have no modification time.
type MultipartFileSystem struct {
	paths fsimpl.PathHelper

	// Form of the parsed multipart message.
	// Form.Value holds the values of the non file form fields,
	// see also FormValue and FormValues. The values stay
	// usable after Close, the uploaded files of Form.File don't.
	Form *multipart.Form

	mtx     sync.RWMutex
	closed  bool
	removed bool                        // the temporary files of Form were removed
	readers map[*multipartFile]struct{} // the open readers of uploaded files
}

// Prefix returns the URI prefix of the file system.
func (f *MultipartFileSystem) Prefix() string {
	return f.paths.URIPrefix
}

// Separator returns the path separator of the file system.
func (f *MultipartFileSystem) Separator() string {
	return f.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (f *MultipartFileSystem) CleanPath(uriParts ...string) string {
	return f.paths.CleanPath(uriParts...)
}

// New returns a MultipartFileSystem for an already parsed multipart form
// with the URI prefix "multipart://" + id, a random id is used if id is empty.
//
// The file system is not registered, pass it to fs.Register
// to access the uploaded files via their URIs.
// Close unregisters the file system and removes
// the temporary files of the form.
func New(form *multipart.Form, id string) (*MultipartFileSystem, error) {
	if form == nil {
		return nil, errors.New("nil multipart.Form")
	}
	if id == "" {
		id = fsimpl.RandomString()
	}
	f := &MultipartFileSystem{
		paths:   fsimpl.PathHelper{URIPrefix: Prefix + id, Rooted: true},
		Form:    form,
		readers: make(map[*multipartFile]struct{}),
	}
	return f, nil
}

// NewFromRequest parses the multipart form of a http.Request
// with http.Request.ParseMultipartForm and returns it
// as MultipartFileSystem, see New.
// Files larger than maxMemory are stored in temporary
// files which are removed by Close.
func NewFromRequest(request *http.Request, maxMemory int64, id string) (*MultipartFileSystem, error) {
	err := request.ParseMultipartForm(maxMemory) //#nosec G120 -- caller bounds memory via maxMemory parameter
	if err != nil {
		return nil, err
	}
	return New(request.MultipartForm, id)
}

// FormValue returns the first value of the form field with the passed name,
// or an empty string if the form has no value for that name.
// The form values are kept in memory, so FormValue
// still works after Close.
func (f *MultipartFileSystem) FormValue(name string) string {
	values := f.Form.Value[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// FormValues returns all values of the form field with the passed name,
// or nil if the form has no value for that name.
// The form values are kept in memory, so FormValues
// still works after Close.
func (f *MultipartFileSystem) FormValues(name string) []string {
	return f.Form.Value[name]
}

// FormFile returns the first file uploaded under name
// or ErrDoesNotExist if there is no file under name.
func (f *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	if err := f.checkClosed(); err != nil {
		return fs.InvalidFile, err
	}
	formFiles := f.Form.File[name]
	if len(formFiles) == 0 {
		return fs.InvalidFile, fs.NewErrDoesNotExist(f.fieldFile(name))
	}
	return f.uploadFile(name, fileNames(formFiles)[0]), nil
}

// FormFiles returns the files uploaded under name,
// or nil if there are none or the file system is closed.
func (f *MultipartFileSystem) FormFiles(name string) (files []fs.File) {
	if f.checkClosed() != nil {
		return nil
	}
	names := fileNames(f.Form.File[name])
	if len(names) == 0 {
		return nil
	}
	files = make([]fs.File, len(names))
	for i, fileName := range names {
		files[i] = f.uploadFile(name, fileName)
	}
	return files
}

// GetMultipartFileHeader returns the multipart.FileHeader of the file
// at filePath, which gives access to the MIME header of the uploaded
// part like its Content-Type.
// Returns ErrDoesNotExist if there is no such file.
func (f *MultipartFileSystem) GetMultipartFileHeader(filePath string) (*multipart.FileHeader, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	parts := f.paths.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	formFile := f.formFile(unescapeField(parts[0]), parts[1])
	if formFile == nil {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	return formFile, nil
}

// fieldDir returns the directory name of a form field,
// see MultipartFileSystem for the escaping.
func fieldDir(field string) string {
	switch field {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return fieldEscaper.Replace(field)
}

// fieldEscaper escapes the escape character and the separator of a field name.
var fieldEscaper = strings.NewReplacer("%", "%25", Separator, "%2F")

// unescapeField returns the form field name of a directory name,
// or the directory name itself if it is not validly escaped.
func unescapeField(dir string) string {
	if field, err := url.PathUnescape(dir); err == nil {
		return field
	}
	return dir
}

// fieldPath returns the path of the directory of a form field.
func fieldPath(field string) string {
	return Separator + fieldDir(field)
}

// uploadPath returns the path of the uploaded file
// with the file system name fileName under the form field.
func uploadPath(field, fileName string) string {
	return fieldPath(field) + Separator + fileName
}

// fieldFile returns the directory of a form field.
func (f *MultipartFileSystem) fieldFile(field string) fs.File {
	return fs.File(f.paths.URL(fieldPath(field)))
}

// uploadFile returns the uploaded file with the
// file system name fileName under the form field.
func (f *MultipartFileSystem) uploadFile(field, fileName string) fs.File {
	return fs.File(f.paths.URL(uploadPath(field, fileName)))
}

// fileNames returns the file system names of the passed uploaded files
// in the same order, see MultipartFileSystem for the naming rules.
func fileNames(formFiles []*multipart.FileHeader) []string {
	names := make([]string, len(formFiles))
	used := make(map[string]bool, len(formFiles))
	for i, formFile := range formFiles {
		name := path.Base(formFile.Filename)
		if name == "." || name == ".." || name == Separator {
			name = unnamedFile
		}
		if used[name] {
			ext := path.Ext(name)
			base := strings.TrimSuffix(name, ext)
			for n := 2; used[name]; n++ {
				name = base + " (" + strconv.Itoa(n) + ")" + ext
			}
		}
		used[name] = true
		names[i] = name
	}
	return names
}

// formFile returns the uploaded file with the file system name
// in the directory of the form field dir,
// or nil if there is no such file.
func (f *MultipartFileSystem) formFile(dir, name string) *multipart.FileHeader {
	formFiles := f.Form.File[dir]
	for i, fileName := range fileNames(formFiles) {
		if fileName == name {
			return formFiles[i]
		}
	}
	return nil
}

// checkClosed returns fs.ErrFileSystemClosed after Close was called.
func (f *MultipartFileSystem) checkClosed() error {
	f.mtx.RLock()
	defer f.mtx.RUnlock()
	if f.closed {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	return nil
}

// RootDir returns the root directory of the file system,
// which lists the form fields that have uploaded files.
func (f *MultipartFileSystem) RootDir() fs.File {
	return fs.File(f.paths.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system,
// which is unique per parsed form.
func (f *MultipartFileSystem) ID() string {
	return f.paths.URIPrefix
}

// ReadableWritable returns true for readable and false for writable,
// because uploaded files can only be read.
func (*MultipartFileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

// Name returns "multipart file system" and the id of the file system.
func (f *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(f.paths.URIPrefix)
}

// String implements the fmt.Stringer interface.
func (f *MultipartFileSystem) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

// File returns a File of this file system for the path,
// which has the form "/<form field>/<file name>".
func (f *MultipartFileSystem) File(filePath string) fs.File {
	return f.JoinCleanFile(filePath)
}

// JoinCleanFile joins the URI parts and returns a File
// with the cleaned path and the prefix of this file system.
func (f *MultipartFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.paths.JoinCleanURI(uriParts...))
}

func (f *MultipartFileSystem) info(filePath string) *fs.FileInfo {
	var info fs.FileInfo
	parts := f.paths.SplitPath(filePath)
	switch len(parts) {
	case 0:
		// The root directory always exists,
		// it contains the form fields with uploaded files
		info.Name = Separator
		info.Exists = true
		info.IsDir = true
	case 1:
		field := unescapeField(parts[0])
		if len(f.Form.File[field]) > 0 {
			info.File = f.fieldFile(field)
			info.Name = fieldDir(field)
			info.Exists = true
			info.IsDir = true
		}
	case 2:
		field, name := unescapeField(parts[0]), parts[1]
		if formFile := f.formFile(field, name); formFile != nil {
			info.File = f.uploadFile(field, name)
			info.Name = name
			info.Exists = true
			info.IsRegular = true
			info.Size = formFile.Size
		}
	}
	if info.Exists {
		if info.File == "" {
			info.File = f.RootDir()
		}
		info.IsHidden = strings.HasPrefix(info.Name, ".")
		// Multipart form files carry no modification time
		info.Permissions = fs.AllRead
	}
	return &info
}

// Stat returns the FileInfo of a form field directory
// or of an uploaded file.
func (f *MultipartFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	info := f.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	return info, nil
}

// Exists reports if the form field directory or uploaded file exists.
func (f *MultipartFileSystem) Exists(filePath string) (bool, error) {
	if err := f.checkClosed(); err != nil {
		return false, err
	}
	return f.info(filePath).Exists, nil
}

// ListDir calls the callback for every form field with uploaded files
// when dirPath is the root directory, or for every file uploaded under
// the form field named by dirPath. Only entries matching any of the
// patterns are reported, or all of them if no patterns are passed.
func (f *MultipartFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) (err error) {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	parts := f.paths.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		// Sorted for a deterministic listing,
		// the form fields are stored in a map
		for _, field := range slices.Sorted(maps.Keys(f.Form.File)) {
			if len(f.Form.File[field]) == 0 {
				continue
			}
			matched, err := fsimpl.MatchAnyPattern(fieldDir(field), patterns)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			err = callback(f.info(fieldPath(field)))
			if err != nil {
				return err
			}
		}
	case 1:
		field := unescapeField(parts[0])
		formFiles := f.Form.File[field]
		if len(formFiles) == 0 {
			return fs.NewErrDoesNotExist(f.File(dirPath))
		}
		for _, name := range fileNames(formFiles) {
			matched, err := fsimpl.MatchAnyPattern(name, patterns)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			err = callback(f.info(uploadPath(field, name)))
			if err != nil {
				return err
			}
		}
	case 2:
		if f.info(dirPath).Exists {
			return fs.NewErrIsNotDirectory(f.File(dirPath))
		}
		return fs.NewErrDoesNotExist(f.File(dirPath))
	default:
		return fs.NewErrDoesNotExist(f.File(dirPath))
	}
	return nil
}

// ReadAll reads the complete content of an uploaded file.
func (f *MultipartFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	file, err := f.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return fs.ReadAll(ctx, file)
}

// OpenReader opens an uploaded file for reading.
// The reader implements io/fs.File, Close of the
// file system closes it if it is still open.
// The root and the form field directories are an fs.ErrIsDirectory.
func (f *MultipartFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	header, err := f.GetMultipartFileHeader(filePath)
	if errors.Is(err, iofs.ErrNotExist) {
		if info := f.info(filePath); info.IsDir {
			return nil, fs.NewErrIsDirectory(info.File)
		}
	}
	if err != nil {
		return nil, err
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	_, name := f.paths.SplitDirAndName(filePath)
	reader := &multipartFile{File: file, fs: f, name: name, header: header}
	f.readers[reader] = struct{}{}
	return reader, nil
}

// Close unregisters the file system, closes the readers of uploaded
// files that are still open and removes the temporary files of the
// multipart form. Afterwards all file system methods that return an
// error return fs.ErrFileSystemClosed; FormValue, FormValues and the
// values of the Form field stay usable.
//
// Close is idempotent. If the temporary files could not be removed,
// the error is returned and the next Close tries again.
func (f *MultipartFileSystem) Close() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if !f.closed {
		f.closed = true
		fs.Unregister(f)
	}
	if f.removed {
		return nil
	}
	// Open files can't be removed on Windows
	var errs []error
	for reader := range f.readers {
		errs = append(errs, reader.closeLocked())
	}
	err := f.Form.RemoveAll()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	f.removed = true
	return errors.Join(errs...)
}

// multipartFile is a reader of an uploaded file.
type multipartFile struct {
	multipart.File

	fs     *MultipartFileSystem
	name   string // file system name, see fileNames
	header *multipart.FileHeader
	closed bool // guarded by fs.mtx
}

// Stat returns the FileInfo of the uploaded file, implementing io/fs.File.
func (f *multipartFile) Stat() (iofs.FileInfo, error) {
	return multipartFileInfo{name: f.name, header: f.header}, nil
}

// Close closes the uploaded file. Close is idempotent.
func (f *multipartFile) Close() error {
	f.fs.mtx.Lock()
	defer f.fs.mtx.Unlock()
	return f.closeLocked()
}

// closeLocked closes the uploaded file. f.fs.mtx must be held.
func (f *multipartFile) closeLocked() error {
	if f.closed {
		return nil
	}
	f.closed = true
	delete(f.fs.readers, f)
	return f.File.Close()
}

type multipartFileInfo struct {
	name   string
	header *multipart.FileHeader
}

func (f multipartFileInfo) Name() string        { return f.name }
func (f multipartFileInfo) Size() int64         { return f.header.Size }
func (f multipartFileInfo) Mode() iofs.FileMode { return fs.AllRead.FileMode(false) }
func (f multipartFileInfo) ModTime() time.Time  { return time.Time{} }
func (f multipartFileInfo) IsDir() bool         { return false }
func (f multipartFileInfo) Sys() any            { return nil }
