package multipartfs

import (
	"context"
	"io"
	iofs "io/fs"
	"mime/multipart"
	"net/http"
	"path"
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
)

var (
	// Make sure MultipartFileSystem implements fs.FileSystem
	_ fs.FileSystem = new(MultipartFileSystem)
)

// MultipartFileSystem wraps the files in a MIME multipart message as fs.FileSystem
type MultipartFileSystem struct {
	fsimpl.PathHelper

	Form *multipart.Form

	closeMtx sync.Mutex
	closed   bool
}

// FromRequestForm returns a MultipartFileSystem from a http.Request
func FromRequestForm(request *http.Request, maxMemory int64) (*MultipartFileSystem, error) {
	err := request.ParseMultipartForm(maxMemory) //#nosec G120 -- caller bounds memory via maxMemory parameter
	if err != nil {
		return nil, err
	}
	f := &MultipartFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		Form:       request.MultipartForm,
	}
	fs.Register(f)
	return f, err
}

// FormFile returns the first file uploaded under name
// or ErrDoesNotExist if there is no file under name.
func (f *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	formFiles, _ := f.Form.File[name]
	if len(formFiles) == 0 {
		return "", fs.NewErrDoesNotExist(f.File(name))
	}
	return f.JoinCleanFile(name, formFiles[0].Filename), nil
}

// FormFiles returns the uploaded files under name.
func (f *MultipartFileSystem) FormFiles(name string) (files []fs.File) {
	formFiles, _ := f.Form.File[name]
	if len(formFiles) == 0 {
		return nil
	}
	files = make([]fs.File, len(formFiles))
	for i, formFile := range formFiles {
		files[i] = f.JoinCleanFile(name, formFile.Filename)
	}
	return files
}

func (f *MultipartFileSystem) GetMultipartFileHeader(filePath string) (*multipart.FileHeader, error) {
	parts := f.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	dir, filename := parts[0], parts[1]
	formFiles, _ := f.Form.File[dir]
	for _, formFile := range formFiles {
		if formFile.Filename == filename {
			return formFile, nil
		}
	}
	return nil, fs.NewErrDoesNotExist(f.File(filePath))
}

func (f *MultipartFileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *MultipartFileSystem) ID() string {
	return f.URIPrefix
}

func (*MultipartFileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

func (f *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(f.URIPrefix)
}

// String implements the fmt.Stringer interface.
func (f *MultipartFileSystem) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

func (f *MultipartFileSystem) File(filePath string) fs.File {
	return f.JoinCleanFile(filePath)
}

func (f *MultipartFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

func (f *MultipartFileSystem) info(filePath string) *fs.FileInfo {
	var info fs.FileInfo
	parts := f.SplitPath(filePath)
	switch len(parts) {
	case 1:
		dir := parts[0]
		exists := len(f.Form.File[dir]) > 0
		if exists {
			info.Name = dir
			info.Exists = true
			info.IsDir = true
		}
	case 2:
		dir, filename := parts[0], parts[1]
		formFiles, _ := f.Form.File[dir]
		for _, formFile := range formFiles {
			if formFile.Filename == filename {
				info.Name = filename
				info.Exists = true
				info.IsRegular = true
				info.Size = formFile.Size
				break
			}
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

func (f *MultipartFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	info := f.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	return info, nil
}

func (f *MultipartFileSystem) Exists(filePath string) (bool, error) {
	parts := f.SplitPath(filePath)
	switch len(parts) {
	case 1:
		dir := parts[0]
		return len(f.Form.File[dir]) > 0, nil
	case 2:
		dir, filename := parts[0], parts[1]
		formFiles, _ := f.Form.File[dir]
		for _, formFile := range formFiles {
			if formFile.Filename == filename {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *MultipartFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	parts := f.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		for fileDir := range f.Form.File {
			info := f.info(fileDir)
			err = callback(info)
			if err != nil {
				return err
			}
		}
	case 1:
		dir := parts[0]
		formFiles, _ := f.Form.File[dir]
		if len(formFiles) == 0 {
			return fs.NewErrDoesNotExist(f.File(dirPath))
		}
		for _, formFile := range formFiles {
			matched, err := f.MatchAnyPattern(formFile.Filename, patterns)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			err = callback(f.info(path.Join(dir, formFile.Filename)))
			if err != nil {
				return err
			}
		}
	case 2:
		if exists, _ := f.Exists(dirPath); exists {
			return fs.NewErrIsNotDirectory(f.File(dirPath))
		}
		return fs.NewErrDoesNotExist(f.File(dirPath))
	default:
		return fs.NewErrDoesNotExist(f.File(dirPath))
	}
	return nil
}

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

func (f *MultipartFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	filePath, err := EscapePath(filePath)
	if err != nil {
		return nil, err
	}
	header, err := f.GetMultipartFileHeader(filePath)
	if err != nil {
		return nil, err
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	return multipartFile{File: file, header: header}, nil
}

// Close unregisters the file system and removes the temporary
// files of the multipart form. It is safe to call Close more than once.
func (f *MultipartFileSystem) Close() error {
	f.closeMtx.Lock()
	defer f.closeMtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	fs.Unregister(f)
	return f.Form.RemoveAll()
}

type multipartFile struct {
	multipart.File

	header *multipart.FileHeader
}

func (f multipartFile) Stat() (iofs.FileInfo, error) {
	return multipartFileInfo{f.header}, nil
}

type multipartFileInfo struct {
	header *multipart.FileHeader
}

func (f multipartFileInfo) Name() string        { return f.header.Filename }
func (f multipartFileInfo) Size() int64         { return f.header.Size }
func (f multipartFileInfo) Mode() iofs.FileMode { return 0666 }
func (f multipartFileInfo) ModTime() time.Time  { return time.Time{} }
func (f multipartFileInfo) IsDir() bool         { return false }
func (f multipartFileInfo) Sys() any            { return nil }

func EscapePath(filePath string) (string, error) {
	// TODO: properly escape paths

	// parsedFilePath, err := url.Parse(filePath)
	// if err != nil {
	// 	return "", err
	// }

	return strings.Replace(filePath, "\"", "%22", -1), nil
}
