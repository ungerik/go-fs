package fs

import (
	"io"
	"path/filepath"
	"time"

	"github.com/satori/go.uuid"
)

// SubFileSystemPrefix is the URI prefix used to identify SubFileSystem files
const SubFileSystemPrefix = "sub://"

var subFileSystems map[string]*SubFileSystem

type SubFileSystem struct {
	prefix   string
	Parent   FileSystem
	BasePath string
}

func NewSubFileSystem(parent FileSystem, basePath string) *SubFileSystem {
	fs := &SubFileSystem{
		prefix:   SubFileSystemPrefix + uuid.NewV4().String(),
		Parent:   parent,
		BasePath: basePath,
	}
	subFileSystems[fs.prefix] = fs
	Registry = append(Registry, fs)
	return fs
}

func (fs *SubFileSystem) Destroy() {
	delete(subFileSystems, fs.prefix)
	DeregisterFileSystem(fs)
}

func (fs *SubFileSystem) IsReadOnly() bool {
	return fs.Parent.IsReadOnly()
}

func (fs *SubFileSystem) Prefix() string {
	return fs.prefix
}

func (fs *SubFileSystem) Name() string {
	return "Sub file system of " + fs.Parent.Name()
}

///////////////////////////////////////////////////
// TODO Replace implementation with real SubFileSystem from here on:
///////////////////////////////////////////////////

func (fs *SubFileSystem) File(uri ...string) File {
	if len(uri) == 0 {
		panic("SubFileSystem uri must not be empty")
	}

	return File(filepath.Clean(filepath.Join(uri...)))
}

func (fs *SubFileSystem) URN(filePath string) string {
	return filepath.ToSlash(filePath)
}

func (fs *SubFileSystem) URL(filePath string) string {
	return LocalPrefix + fs.URN(filePath)
}

func (fs *SubFileSystem) CleanPath(uri ...string) string {
	return fs.prefix + fs.Parent.CleanPath(uri...)
}

func (fs *SubFileSystem) SplitPath(filePath string) []string {
	return fs.Parent.SplitPath(filePath)
}

func (fs *SubFileSystem) Seperator() string {
	return fs.Parent.Seperator()
}

func (fs *SubFileSystem) FileName(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) Ext(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) Dir(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) Exists(filePath string) bool {
	panic("not implemented")
}

func (fs *SubFileSystem) IsDir(filePath string) bool {
	panic("not implemented")
}

func (fs *SubFileSystem) Size(filePath string) int64 {
	panic("not implemented")
}

func (fs *SubFileSystem) ModTime(filePath string) time.Time {
	panic("not implemented")
}

func (fs *SubFileSystem) ListDir(filePath string, callback func(File) error, patterns ...string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) ListDirMax(filePath string, n int, patterns ...string) (files []File, err error) {
	panic("not implemented")
}

func (fs *SubFileSystem) Permissions(filePath string) Permissions {
	panic("not implemented")
}

func (fs *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	panic("not implemented")
}

func (fs *SubFileSystem) User(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Touch(filePath string, perm ...Permissions) error {
	panic("not implemented")
}

func (fs *SubFileSystem) MakeDir(filePath string, perm ...Permissions) error {
	panic("not implemented")
}

func (fs *SubFileSystem) ReadAll(filePath string) ([]byte, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) WriteAll(filePath string, data []byte, perm ...Permissions) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Append(filePath string, data []byte, perm ...Permissions) error {
	writer, err := fs.OpenAppendWriter(filePath, perm...)
	if err != nil {
		return err
	}
	defer writer.Close()
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (fs *SubFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) OpenWriter(filePath string, perm ...Permissions) (WriteSeekCloser, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) OpenAppendWriter(filePath string, perm ...Permissions) (io.WriteCloser, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) OpenReadWriter(filePath string, perm ...Permissions) (ReadWriteSeekCloser, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	panic("not implemented")
}

func (fs *SubFileSystem) Truncate(filePath string, size int64) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Rename(filePath string, newName string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Move(filePath string, destPath string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Remove(filePath string) error {
	panic("not implemented")
}
