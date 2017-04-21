package fs

import (
	"io"
	"path/filepath"

	"github.com/satori/go.uuid"
)

// SubFileSystemPrefix is the URI prefix used to identify SubFileSystem files
const SubFileSystemPrefix = "sub://"

type SubFileSystem struct {
	prefix   string
	Parent   FileSystem
	BasePath string
}

func NewSubFileSystem(parent FileSystem, basePath string) *SubFileSystem {
	subfs := &SubFileSystem{
		prefix:   SubFileSystemPrefix + uuid.NewV4().String(),
		Parent:   parent,
		BasePath: basePath,
	}
	Register(subfs)
	return subfs
}

func (subfs *SubFileSystem) Destroy() error {
	Unregister(subfs)
	return nil
}

func (subfs *SubFileSystem) IsReadOnly() bool {
	return subfs.Parent.IsReadOnly()
}

func (subfs *SubFileSystem) Prefix() string {
	return subfs.prefix
}

func (subfs *SubFileSystem) Name() string {
	return "Sub file system of " + subfs.Parent.Name()
}

func (subfs *SubFileSystem) String() string {
	return subfs.Name() + " with prefix " + subfs.Prefix()
}


///////////////////////////////////////////////////
// TODO Replace implementation with real SubFileSystem from here on:
///////////////////////////////////////////////////

func (subfs *SubFileSystem) File(uri ...string) File {
	if len(uri) == 0 {
		panic("SubFileSystem uri must not be empty")
	}

	return File(filepath.Clean(filepath.Join(uri...)))
}

func (subfs *SubFileSystem) URN(filePath string) string {
	return filepath.ToSlash(filePath)
}

func (subfs *SubFileSystem) URL(filePath string) string {
	return LocalPrefix + subfs.URN(filePath)
}

func (subfs *SubFileSystem) CleanPath(uri ...string) string {
	return subfs.prefix + subfs.Parent.CleanPath(uri...)
}

func (subfs *SubFileSystem) SplitPath(filePath string) []string {
	return subfs.Parent.SplitPath(filePath)
}

func (subfs *SubFileSystem) Seperator() string {
	return subfs.Parent.Seperator()
}

func (subfs *SubFileSystem) FileName(filePath string) string {
	panic("not implemented")
}

func (subfs *SubFileSystem) Ext(filePath string) string {
	panic("not implemented")
}

func (subfs *SubFileSystem) Dir(filePath string) string {
	panic("not implemented")
}

// Stat returns FileInfo
func (subfs *SubFileSystem) Stat(filePath string) FileInfo {
	panic("not implemented")
}

func (subfs *SubFileSystem) ListDir(dirPath string, callback func(File) error, patterns []string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []File, err error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) User(filePath string) string {
	panic("not implemented")
}

func (subfs *SubFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (subfs *SubFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Touch(filePath string, perm []Permissions) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) MakeDir(filePath string, perm []Permissions) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) ReadAll(filePath string) ([]byte, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) WriteAll(filePath string, data []byte, perm []Permissions) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Append(filePath string, data []byte, perm []Permissions) error {
	writer, err := subfs.OpenAppendWriter(filePath, perm)
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

func (subfs *SubFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteSeekCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) Truncate(filePath string, size int64) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) CopyFile(srcFile string, destFile string, buf *[]byte) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Rename(filePath string, newName string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Move(filePath string, destPath string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Remove(filePath string) error {
	panic("not implemented")
}
