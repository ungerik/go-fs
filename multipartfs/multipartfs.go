package multipartfs

import (
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"path"
	"time"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix for the MultipartFileSystem
	Prefix = "multipart://"

	// Separator used in MultipartFileSystem paths
	Separator = "/"
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

func (mpfs *MultipartFileSystem) Root() fs.File {
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

func (*MultipartFileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*MultipartFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (*MultipartFileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, 0, Separator)
}

func (*MultipartFileSystem) VolumeName(filePath string) string {
	return ""
}

func (mpfs *MultipartFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (mpfs *MultipartFileSystem) AbsPath(filePath string) string {
	if mpfs.IsAbsPath(filePath) {
		return filePath
	}
	return Separator + filePath
}

// Stat returns FileInfo
func (mpfs *MultipartFileSystem) Stat(filePath string) (info fs.FileInfo) {
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
		info.IsRegular = true
		info.Size = -1
		// TODO get time from header if exists
		info.ModTime = time.Now()
		info.Permissions = fs.AllRead
	}
	return info
}

func (mpfs *MultipartFileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (mpfs *MultipartFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (mpfs *MultipartFileSystem) ListDirInfo(dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	parts := mpfs.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		for fileDir := range mpfs.Form.File {
			file := mpfs.File(fileDir)
			info := mpfs.Stat(fileDir)
			err = callback(file, info)
			if err != nil {
				return err
			}
		}
	case 1:
		dir := parts[0]
		ff, _ := mpfs.Form.File[dir]
		if len(ff) > 0 {
			for _, f := range ff {
				file := mpfs.JoinCleanFile(dir, f.Filename)
				info := mpfs.Stat(file.Path())
				err = callback(file, info)
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

func (mpfs *MultipartFileSystem) ListDirInfoRecursive(dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) error {
	return fs.ListDirInfoRecursiveImpl(mpfs, dirPath, callback, patterns)
}

func (mpfs *MultipartFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(max, func(callback func(fs.File) error) error {
		return mpfs.ListDirInfo(dirPath, fs.FileCallback(callback).FileInfoCallback, patterns)
	})
}

func (*MultipartFileSystem) User(filePath string) string {
	return ""
}

func (*MultipartFileSystem) Group(filePath string) string {
	return ""
}

func (mpfs *MultipartFileSystem) ReadAll(filePath string) ([]byte, error) {
	file, err := mpfs.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return ioutil.ReadAll(file)
}

func (mpfs *MultipartFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	file, err := mpfs.GetMultipartFileHeader(filePath)
	if err != nil {
		return nil, err
	}
	return file.Open()
}
