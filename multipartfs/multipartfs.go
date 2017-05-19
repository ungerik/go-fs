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
)

// Prefix for the MultipartFileSystem
const Prefix = "multipart://"

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

func (mpfs *MultipartFileSystem) File(uriParts ...string) fs.File {
	return fs.File(mpfs.prefix + mpfs.CleanPath(uriParts...))
}

func (mpfs *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	f, _ := mpfs.Form.File[name]
	if len(f) == 0 {
		return "", fs.NewErrDoesNotExist(mpfs.File(name))
	}
	return mpfs.File(name, f[0].Filename), nil
}

func (mpfs *MultipartFileSystem) URL(cleanPath string) string {
	return mpfs.prefix + url.PathEscape(cleanPath)
}

func (mpfs *MultipartFileSystem) CleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], mpfs.prefix)
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath)
	return cleanPath
}

func (mpfs *MultipartFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, mpfs.prefix)
	filePath = strings.TrimPrefix(filePath, "/")
	return strings.Split(filePath, "/")
}

func (*MultipartFileSystem) Seperator() string {
	return "/"
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*MultipartFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fs.MatchAnyPatternImpl(name, patterns)
}

func (*MultipartFileSystem) FileName(filePath string) string {
	return path.Base(filePath)
}

func (*MultipartFileSystem) Ext(filePath string) string {
	return path.Ext(filePath)
}

func (*MultipartFileSystem) Dir(filePath string) string {
	return path.Dir(filePath)
}

// Stat returns FileInfo
func (mpfs *MultipartFileSystem) Stat(filePath string) (info fs.FileInfo) {
	parts := mpfs.SplitPath(filePath)
	switch len(parts) {
	case 1:
		info.Exists = len(mpfs.Form.File[parts[0]]) > 0
		info.IsDir = info.Exists
	case 2:
		f, _ := mpfs.Form.File[parts[0]]
		info.Exists = len(f) > 0 && f[0].Filename == parts[1]
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
		for fileDir, _ := range mpfs.Form.File {
			err = callback(mpfs.File(fileDir))
			if err != nil {
				return err
			}
		}
	case 1:
		fileDir := parts[0]
		f, _ := mpfs.Form.File[fileDir]
		if len(f) > 0 {
			err = callback(mpfs.File(fileDir, f[0].Filename))
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

func (mpfs *MultipartFileSystem) ListDirRecursive(dirPath string, callback func(fs.File) error, patterns []string) error {
	return fs.ListDirRecursiveImpl(mpfs, dirPath, callback, patterns)
}

func (mpfs *MultipartFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	listDirFunc := fs.ListDirFunc(func(callback func(fs.File) error) error {
		return mpfs.ListDir(dirPath, callback, patterns)
	})
	return listDirFunc.ListDirMaxImpl(max)
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
	parts := mpfs.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(mpfs.File(filePath))
	}
	f, _ := mpfs.Form.File[parts[0]]
	if len(f) == 0 || f[0].Filename != parts[1] {
		return nil, fs.NewErrDoesNotExist(mpfs.File(filePath))
	}
	return f[0].Open()
}
