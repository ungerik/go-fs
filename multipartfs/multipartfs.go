package multipartfs

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	uuid "github.com/satori/go.uuid"
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
	mfs := &MultipartFileSystem{
		prefix: Prefix + uuid.NewV4().String(),
		Form:   request.MultipartForm,
	}
	return mfs, err
}

func (mfs *MultipartFileSystem) Destroy() error {
	// delete(fileSystems, mfs.prefix)
	fs.DeregisterFileSystem(mfs)
	return mfs.Form.RemoveAll()
}

// Prefix for the MultipartFileSystem
func (mfs *MultipartFileSystem) Prefix() string {
	return mfs.prefix
}

func (mfs *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(mfs.prefix)
}

func (mfs *MultipartFileSystem) File(uri ...string) fs.File {
	return fs.File(path.Join(uri...))
}

func (mfs *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	f, _ := mfs.Form.File[name]
	if len(f) == 0 {
		return "", errors.New("form file not found: " + name)
	}
	return fs.File(path.Join(mfs.prefix, name, f[0].Filename)), nil
}

func (mfs *MultipartFileSystem) URN(filePath string) string {
	return filePath
}

func (mfs *MultipartFileSystem) URL(filePath string) string {
	return path.Join(mfs.prefix, filePath)
}

func (mfs *MultipartFileSystem) CleanPath(uri ...string) string {
	return strings.TrimPrefix(path.Join(uri...), mfs.prefix)
}

func (mfs *MultipartFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, mfs.prefix)
	filePath = strings.TrimPrefix(filePath, "/")
	return strings.Split(filePath, "/")
}

func (*MultipartFileSystem) Seperator() string {
	return "/"
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

func (mfs *MultipartFileSystem) Exists(filePath string) bool {
	parts := mfs.SplitPath(filePath)
	switch len(parts) {
	case 1:
		return len(mfs.Form.File[parts[0]]) > 0
	case 2:
		f, _ := mfs.Form.File[parts[0]]
		return len(f) > 0 && f[0].Filename == parts[1]
	}
	return false
}

func (mfs *MultipartFileSystem) IsDir(filePath string) bool {
	parts := mfs.SplitPath(filePath)
	return len(parts) == 1 && len(mfs.Form.File[parts[0]]) > 0
}

func (mfs *MultipartFileSystem) Size(filePath string) int64 {
	return -1
}

func (mfs *MultipartFileSystem) ModTime(filePath string) time.Time {
	// TODO get time from header if exists
	return time.Now()
}

func (mfs *MultipartFileSystem) ListDir(filePath string, callback func(fs.File) error, patterns ...string) (err error) {
	parts := mfs.SplitPath(filePath)
	switch len(parts) {
	case 0:
		for name, _ := range mfs.Form.File {
			err = callback(fs.File(mfs.prefix + name))
			if err != nil {
				return err
			}
		}
	case 1:
		name := parts[0]
		f, _ := mfs.Form.File[name]
		if len(f) > 0 {
			err = callback(fs.File(mfs.prefix + name + "/" + f[0].Filename))
		} else {
			err = fs.NewErrFileDoesNotExist(fs.File(filePath))
		}
	case 2:
		err = fs.NewErrIsNotDirectory(fs.File(filePath))
	default:
		err = fs.NewErrFileDoesNotExist(fs.File(filePath))
	}
	return err
}

func (mfs *MultipartFileSystem) ListDirMax(filePath string, n int, patterns ...string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(mfs, filePath, n, patterns...)
}

func (*MultipartFileSystem) Permissions(filePath string) fs.Permissions {
	return fs.AllRead
}

func (*MultipartFileSystem) User(filePath string) string {
	return ""
}

func (*MultipartFileSystem) Group(filePath string) string {
	return ""
}

func (mfs *MultipartFileSystem) ReadAll(filePath string) ([]byte, error) {
	file, err := mfs.OpenReader(filePath)
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

func (mfs *MultipartFileSystem) OpenReader(filePath string) (fs.ReadSeekCloser, error) {
	parts := mfs.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrFileDoesNotExist(fs.File(filePath))
	}
	f, _ := mfs.Form.File[parts[0]]
	if len(f) == 0 || f[0].Filename != parts[1] {
		return nil, fs.NewErrFileDoesNotExist(fs.File(filePath))
	}
	return f[0].Open()
}
