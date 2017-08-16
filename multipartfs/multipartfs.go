package multipartfs

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/satori/go.uuid"
	"github.com/ungerik/go-fs"
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
		prefix: Prefix + uuid.NewV4().String(),
		Form:   request.MultipartForm,
	}
	fs.Register(mpfs)
	return mpfs, err
}

func (mpfs *MultipartFileSystem) Destroy() error {
	fs.Unregister(mpfs)
	return mpfs.Form.RemoveAll()
}

// FormFile returns the first file uploaded under name
// or ErrDoesNotExist if there is no file under name.
func (mpfs *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	ff, _ := mpfs.Form.File[name]
	if len(ff) == 0 {
		return "", fs.NewErrDoesNotExist(mpfs.JoinCleanFile(name))
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
		return nil, fs.NewErrDoesNotExist(mpfs.JoinCleanFile(filePath))
	}
	dir, filename := parts[0], parts[1]
	ff, _ := mpfs.Form.File[dir]
	for _, f := range ff {
		if f.Filename == filename {
			return f, nil
		}
	}
	return nil, fs.NewErrDoesNotExist(mpfs.JoinCleanFile(filePath))
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

func (mpfs *MultipartFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(mpfs.prefix + mpfs.JoinCleanPath(uriParts...))
}

func (mpfs *MultipartFileSystem) URL(cleanPath string) string {
	return mpfs.prefix + cleanPath
}

func (mpfs *MultipartFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], mpfs.prefix)
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	return path.Clean(cleanPath)
}

func (mpfs *MultipartFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, mpfs.prefix)
	filePath = strings.TrimPrefix(filePath, Separator)
	filePath = strings.TrimSuffix(filePath, Separator)
	return strings.Split(filePath, Separator)
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
		info.Exists = len(mpfs.Form.File[dir]) > 0
		info.IsDir = info.Exists
	case 2:
		dir, filename := parts[0], parts[1]
		ff, _ := mpfs.Form.File[dir]
		for _, f := range ff {
			if f.Filename == filename {
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

func (mpfs *MultipartFileSystem) ListDir(dirPath string, callback func(fs.File) error, patterns []string) (err error) {
	parts := mpfs.SplitPath(dirPath)
	switch len(parts) {
	case 0:
		for fileDir := range mpfs.Form.File {
			err = callback(mpfs.JoinCleanFile(fileDir))
			if err != nil {
				return err
			}
		}
	case 1:
		dir := parts[0]
		ff, _ := mpfs.Form.File[dir]
		if len(ff) > 0 {
			for _, f := range ff {
				err = callback(mpfs.JoinCleanFile(dir, f.Filename))
				if err != nil {
					return err
				}
			}
		} else {
			err = fs.NewErrDoesNotExist(mpfs.JoinCleanFile(dirPath))
		}
	case 2:
		err = fs.NewErrIsNotDirectory(mpfs.JoinCleanFile(dirPath))
	default:
		err = fs.NewErrDoesNotExist(mpfs.JoinCleanFile(dirPath))
	}
	return err
}

func (mpfs *MultipartFileSystem) ListDirRecursive(dirPath string, callback func(fs.File) error, patterns []string) error {
	return fs.ListDirRecursiveImpl(mpfs, dirPath, callback, patterns)
}

func (mpfs *MultipartFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(max, func(callback func(fs.File) error) error {
		return mpfs.ListDir(dirPath, callback, patterns)
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
	var buf bytes.Buffer
	_, err = io.Copy(&buf, file)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (mpfs *MultipartFileSystem) OpenReader(filePath string) (fs.ReadSeekCloser, error) {
	file, err := mpfs.GetMultipartFileHeader(filePath)
	if err != nil {
		return nil, err
	}
	return file.Open()
}
