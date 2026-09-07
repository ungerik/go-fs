// Package multipartfs implements a read-only file system
// for the files of a parsed multipart HTTP form.
//
// FromRequestForm parses the form of a request; the uploaded
// files are then accessible as fs.File values and the form
// values via the Form field. Close removes the temporary files
// of the form.
package multipartfs

import (
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"mime/multipart"
	"net/http"
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
// so the file system has exactly two levels.
//
// Because a file system path has to identify exactly one file, the names of
// the uploaded files are made unique per directory: the second file with an
// already used name gets a " (2)" suffix before its extension, the third a
// " (3)" and so on. Uploaded names that are not usable as a path element
// (like "." and "..") are replaced by "unnamed".
type MultipartFileSystem struct {
	fsimpl.PathHelper

	// Form of the parsed multipart message.
	// Form.Value holds the values of the non file form fields,
	// see also FormValue and FormValues.
	Form *multipart.Form

	mtx    sync.RWMutex
	closed bool
}

// New returns a MultipartFileSystem for an already parsed multipart form
// and registers it. form must not be nil.
// Close unregisters the file system and removes
// the temporary files of the form.
func New(form *multipart.Form) *MultipartFileSystem {
	if form == nil {
		panic("nil multipart.Form")
	}
	f := &MultipartFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		Form:       form,
	}
	fs.Register(f)
	return f
}

// FromRequestForm parses the multipart form of a http.Request
// with http.Request.ParseMultipartForm and returns it
// as registered MultipartFileSystem.
// Files larger than maxMemory are stored in temporary
// files which are removed by Close.
func FromRequestForm(request *http.Request, maxMemory int64) (*MultipartFileSystem, error) {
	err := request.ParseMultipartForm(maxMemory) //#nosec G120 -- caller bounds memory via maxMemory parameter
	if err != nil {
		return nil, err
	}
	return New(request.MultipartForm), nil
}

// FormValue returns the first value of the form field with the passed name,
// or an empty string if the form has no value for that name.
func (f *MultipartFileSystem) FormValue(name string) string {
	values := f.Form.Value[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// FormValues returns all values of the form field with the passed name,
// or nil if the form has no value for that name.
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
		return fs.InvalidFile, fs.NewErrDoesNotExist(f.File(name))
	}
	return f.JoinCleanFile(name, fileNames(formFiles)[0]), nil
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
		files[i] = f.JoinCleanFile(name, fileName)
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
	parts := f.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	formFile := f.formFile(parts[0], parts[1])
	if formFile == nil {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	return formFile, nil
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
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system,
// which is unique per parsed form.
func (f *MultipartFileSystem) ID() string {
	return f.URIPrefix
}

// ReadableWritable returns true for readable and false for writable,
// because uploaded files can only be read.
func (*MultipartFileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

// Name returns "multipart file system" and the id of the file system.
func (f *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(f.URIPrefix)
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
	return fs.File(f.JoinCleanURI(uriParts...))
}

func (f *MultipartFileSystem) info(filePath string) *fs.FileInfo {
	var info fs.FileInfo
	parts := f.SplitPath(filePath)
	switch len(parts) {
	case 0:
		// The root directory always exists,
		// it contains the form fields with uploaded files
		info.Name = Separator
		info.Exists = true
		info.IsDir = true
	case 1:
		dir := parts[0]
		if len(f.Form.File[dir]) > 0 {
			info.Name = dir
			info.Exists = true
			info.IsDir = true
		}
	case 2:
		dir, name := parts[0], parts[1]
		if formFile := f.formFile(dir, name); formFile != nil {
			info.Name = name
			info.Exists = true
			info.IsRegular = true
			info.Size = formFile.Size
		}
	}
	if info.Exists {
		info.File = f.JoinCleanFile(filePath)
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

	parts := f.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		// Sorted for a deterministic listing,
		// the form fields are stored in a map
		for _, dir := range slices.Sorted(maps.Keys(f.Form.File)) {
			if len(f.Form.File[dir]) == 0 {
				continue
			}
			matched, err := fsimpl.MatchAnyPattern(dir, patterns)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			err = callback(f.info(dir))
			if err != nil {
				return err
			}
		}
	case 1:
		dir := parts[0]
		formFiles := f.Form.File[dir]
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
			err = callback(f.info(path.Join(dir, name)))
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

	return fs.ReadAllContext(ctx, file)
}

// OpenReader opens an uploaded file for reading.
func (f *MultipartFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	header, err := f.GetMultipartFileHeader(filePath)
	if err != nil {
		return nil, err
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	_, name := f.SplitDirAndName(filePath)
	return multipartFile{File: file, name: name, header: header}, nil
}

// Close unregisters the file system and removes the temporary
// files of the multipart form. It is safe to call Close more than once.
// All file system methods return fs.ErrFileSystemClosed afterwards.
func (f *MultipartFileSystem) Close() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	fs.Unregister(f)
	return f.Form.RemoveAll()
}

type multipartFile struct {
	multipart.File

	name   string // file system name, see fileNames
	header *multipart.FileHeader
}

func (f multipartFile) Stat() (iofs.FileInfo, error) {
	return multipartFileInfo{name: f.name, header: f.header}, nil
}

type multipartFileInfo struct {
	name   string
	header *multipart.FileHeader
}

func (f multipartFileInfo) Name() string        { return f.name }
func (f multipartFileInfo) Size() int64         { return f.header.Size }
func (f multipartFileInfo) Mode() iofs.FileMode { return 0666 }
func (f multipartFileInfo) ModTime() time.Time  { return time.Time{} }
func (f multipartFileInfo) IsDir() bool         { return false }
func (f multipartFileInfo) Sys() any            { return nil }
