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
	mpfs := &MultipartFileSystem{
		prefix: Prefix + uuid.NewV4().String(),
		Form:   request.MultipartForm,
	}
	return mpfs, err
}

func (mpfs *MultipartFileSystem) Destroy() error {
	// delete(fileSystems, mpfs.prefix)
	fs.DeregisterFileSystem(mpfs)
	return mpfs.Form.RemoveAll()
}

// Prefix for the MultipartFileSystem
func (mpfs *MultipartFileSystem) Prefix() string {
	return mpfs.prefix
}

func (mpfs *MultipartFileSystem) Name() string {
	return "multipart file system " + path.Base(mpfs.prefix)
}

func (mpfs *MultipartFileSystem) File(uriParts ...string) fs.File {
	return fs.File(mpfs.prefix + mpfs.CleanPath(uriParts...))
}

func (mpfs *MultipartFileSystem) FormFile(name string) (fs.File, error) {
	f, _ := mpfs.Form.File[name]
	if len(f) == 0 {
		return "", errors.New("form file not found: " + name)
	}
	return fs.File(path.Join(mpfs.prefix, name, f[0].Filename)), nil
}

func (mpfs *MultipartFileSystem) URL(cleanPath string) string {
	return mpfs.prefix + cleanPath
}

func (mpfs *MultipartFileSystem) CleanPath(uri ...string) string {
	return strings.TrimPrefix(path.Join(uri...), mpfs.prefix)
}

func (mpfs *MultipartFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, mpfs.prefix)
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
		for name, _ := range mpfs.Form.File {
			err = callback(fs.File(mpfs.prefix + name))
			if err != nil {
				return err
			}
		}
	case 1:
		name := parts[0]
		f, _ := mpfs.Form.File[name]
		if len(f) > 0 {
			err = callback(fs.File(mpfs.prefix + name + "/" + f[0].Filename))
		} else {
			err = fs.NewErrDoesNotExist(fs.File(dirPath))
		}
	case 2:
		err = fs.NewErrIsNotDirectory(fs.File(dirPath))
	default:
		err = fs.NewErrDoesNotExist(fs.File(dirPath))
	}
	return err
}

func (mpfs *MultipartFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(dirPath, max, patterns, func(dirPath string, callback func(fs.File) error, patterns []string) error {
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
	parts := mpfs.SplitPath(filePath)
	if len(parts) != 2 {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	f, _ := mpfs.Form.File[parts[0]]
	if len(f) == 0 || f[0].Filename != parts[1] {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return f[0].Open()
}
