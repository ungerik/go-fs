package multipartfs

import (
	"context"
	iofs "io/fs"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
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
	fs.ReadOnlyBase

	prefix string
	Form   *multipart.Form
}

// FromRequestForm returns a MultipartFileSystem from a http.Request
func FromRequestForm(request *http.Request, maxMemory int64) (*MultipartFileSystem, error) {
	err := request.ParseMultipartForm(maxMemory)
	if err != nil {
		return nil, err
	}
	mpfs := &MultipartFileSystem{
		prefix: Prefix + fsimpl.RandomString(),
		Form:   request.MultipartForm,
	}
	fs.Register(mpfs)
	return mpfs, err
}

func (mpfs *MultipartFileSystem) Close() error {
	fs.Unregister(mpfs)
	return mpfs.Form.RemoveAll()
}

// FormFile returns the first file uploaded under name
// or ErrDoesNotExist if there is no file under name.
func (mpfs *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	ff, _ := mpfs.Form.File[name]
	if len(ff) == 0 {
		return "", fs.NewErrDoesNotExist(mpfs.File(name))
	}
	return mpfs.JoinCleanFile(name, ff[0].Filename), nil
}

// FormFiles returns the uploaded files under name.
func (mpfs *MultipartFileSystem) FormFiles(name string) (files []fs.File) {
	ff, _ := mpfs.Form.File[name]
	if len(ff) == 0 {
		return nil
	}
	files = make([]fs.File, len(ff))
	for i, f := range ff {
		files[i] = mpfs.JoinCleanFile(name, f.Filename)
	}
	return files
}

func (mpfs *MultipartFileSystem) GetMultipartFileHeader(filePath string) (*multipart.FileHeader, error) {
	parts := mpfs.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(mpfs.File(filePath))
	}
	dir, filename := parts[0], parts[1]
	ff, _ := mpfs.Form.File[dir]
	for _, f := range ff {
		if f.Filename == filename {
			return f, nil
		}
	}
	return nil, fs.NewErrDoesNotExist(mpfs.File(filePath))
}

func (mpfs *MultipartFileSystem) RootDir() fs.File {
	return fs.File(mpfs.prefix + Separator)
}

func (mpfs *MultipartFileSystem) ID() (string, error) {
	return mpfs.prefix, nil
}

// Prefix for the MultipartFileSystem
func (mpfs *MultipartFileSystem) Prefix() string {
	return mpfs.prefix
}

func (mpfs *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(mpfs.prefix)
}

// String implements the fmt.Stringer interface.
func (mpfs *MultipartFileSystem) String() string {
	return mpfs.Name() + " with prefix " + mpfs.Prefix()
}

func (mpfs *MultipartFileSystem) File(filePath string) fs.File {
	return mpfs.JoinCleanFile(filePath)
}

func (mpfs *MultipartFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(mpfs.prefix + mpfs.JoinCleanPath(uriParts...))
}

func (mpfs *MultipartFileSystem) URL(cleanPath string) string {
	return mpfs.prefix + cleanPath
}

func (mpfs *MultipartFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, mpfs.prefix, Separator)
}

func (mpfs *MultipartFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, mpfs.prefix, Separator)
}

func (*MultipartFileSystem) Separator() string { return Separator }

func (*MultipartFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (mpfs *MultipartFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (mpfs *MultipartFileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

func (mpfs *MultipartFileSystem) info(filePath string) (info fs.FileInfo) {
	parts := mpfs.SplitPath(filePath)
	switch len(parts) {
	case 1:
		dir := parts[0]
		exists := len(mpfs.Form.File[dir]) > 0
		if exists {
			info.Name = dir
			info.Exists = true
			info.IsDir = true
		}
	case 2:
		dir, filename := parts[0], parts[1]
		ff, _ := mpfs.Form.File[dir]
		for _, f := range ff {
			if f.Filename == filename {
				info.Name = filename
				info.Exists = true
				break
			}
		}
	}
	if info.Exists {
		info.File = fs.File(filePath)
		info.IsRegular = true
		info.Size = -1
		// TODO get time from header if exists
		info.Modified = time.Now()
		info.Permissions = fs.AllRead
	}
	return info
}

func (mpfs *MultipartFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info := mpfs.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

func (mpfs *MultipartFileSystem) Exists(filePath string) bool {
	return mpfs.info(filePath).Exists
}

func (mpfs *MultipartFileSystem) IsHidden(filePath string) bool {
	return strings.HasPrefix(path.Base(filePath), ".")
}

func (mpfs *MultipartFileSystem) IsSymbolicLink(filePath string) bool { return false }

func (mpfs *MultipartFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(fs.FileInfo) error, patterns []string) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	parts := mpfs.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		for fileDir := range mpfs.Form.File {
			info := mpfs.info(fileDir)
			err = callback(info)
			if err != nil {
				return err
			}
		}
	case 1:
		dir := parts[0]
		ff, _ := mpfs.Form.File[dir]
		if len(ff) > 0 {
			for _, f := range ff {
				filePath := path.Join(dir, f.Filename)
				info := mpfs.info(filePath)
				err = callback(info)
				if err != nil {
					return err
				}
			}
		} else {
			err = fs.NewErrDoesNotExist(mpfs.File(dirPath))
		}
	case 2:
		err = fs.NewErrIsNotDirectory(mpfs.File(dirPath))
	default:
		err = fs.NewErrDoesNotExist(mpfs.File(dirPath))
	}
	return err
}

func (mpfs *MultipartFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(fs.FileInfo) error, patterns []string) error {
	return fs.ListDirInfoRecursiveImpl(ctx, mpfs, dirPath, callback, patterns)
}

func (mpfs *MultipartFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(ctx, max, func(ctx context.Context, callback func(fs.File) error) error {
		return mpfs.ListDirInfo(ctx, dirPath, fs.FileInfoCallback(callback), patterns)
	})
}

func (mpfs *MultipartFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	file, err := mpfs.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return fs.ReadAllContext(ctx, file)
}

func (mpfs *MultipartFileSystem) OpenReader(filePath string) (iofs.File, error) {
	filePath, err := EscapePath(filePath)
	if err != nil {
		return nil, err
	}
	header, err := mpfs.GetMultipartFileHeader(filePath)
	if err != nil {
		return nil, err
	}
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	return multipartFile{File: file, header: header}, nil
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
func (f multipartFileInfo) ModTime() time.Time  { return time.Now() }
func (f multipartFileInfo) IsDir() bool         { return false }
func (f multipartFileInfo) Sys() interface{}    { return nil }

func EscapePath(filePath string) (string, error) {
	// TODO: properly escape paths

	// parsedFilePath, err := url.Parse(filePath)
	// if err != nil {
	// 	return "", err
	// }

	return strings.Replace(filePath, "\"", "%22", -1), nil
}
