package fs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ungerik/go-fs/fsimpl"
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
		prefix:   SubFileSystemPrefix + fsimpl.RandomString(),
		Parent:   parent,
		BasePath: basePath,
	}
	Register(subfs)
	return subfs
}

func (subfs *SubFileSystem) Close() error {
	Unregister(subfs)
	return nil
}

func (subfs *SubFileSystem) IsReadOnly() bool {
	return subfs.Parent.IsReadOnly()
}

func (subfs *SubFileSystem) IsWriteOnly() bool {
	return subfs.Parent.IsWriteOnly()
}

func (subfs *SubFileSystem) Root() File {
	return File(subfs.Parent.Separator())
}

func (subfs *SubFileSystem) ID() (string, error) {
	parentID, err := subfs.Parent.ID()
	if err != nil {
		return "", err
	}
	return parentID + "/" + subfs.BasePath, nil
}

func (subfs *SubFileSystem) Prefix() string {
	return subfs.prefix
}

func (subfs *SubFileSystem) Name() string {
	return "Sub file system of " + subfs.Parent.Name()
}

// String implements the fmt.Stringer interface.
func (subfs *SubFileSystem) String() string {
	return subfs.Name() + " with prefix " + subfs.Prefix()
}

///////////////////////////////////////////////////
// TODO Replace implementation with real SubFileSystem from here on:
///////////////////////////////////////////////////

func (subfs *SubFileSystem) JoinCleanFile(uri ...string) File {
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

func (subfs *SubFileSystem) JoinCleanPath(uri ...string) string {
	return subfs.prefix + subfs.Parent.JoinCleanPath(uri...)
}

func (subfs *SubFileSystem) SplitPath(filePath string) []string {
	return subfs.Parent.SplitPath(filePath)
}

func (subfs *SubFileSystem) Separator() string {
	return subfs.Parent.Separator()
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (subfs *SubFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return subfs.Parent.MatchAnyPattern(name, patterns)
}

func (subfs *SubFileSystem) DirAndName(filePath string) (dir, name string) {
	panic("not implemented")
}

func (subfs *SubFileSystem) VolumeName(filePath string) string {
	return subfs.Parent.VolumeName(filePath)
}

func (subfs *SubFileSystem) IsAbsPath(filePath string) bool {
	return subfs.Parent.IsAbsPath(filePath)
}

func (subfs *SubFileSystem) AbsPath(filePath string) string {
	return subfs.Parent.AbsPath(filePath)
}

func (subfs *SubFileSystem) Stat(filePath string) (os.FileInfo, error) {
	return subfs.Parent.Stat(filePath)
}

func (subfs *SubFileSystem) Exists(filePath string) bool {
	return subfs.Parent.Exists(filePath)
}

func (subfs *SubFileSystem) IsHidden(filePath string) bool {
	return subfs.Parent.IsHidden(filePath)
}

func (subfs *SubFileSystem) IsSymbolicLink(filePath string) bool {
	return subfs.Parent.IsSymbolicLink(filePath)
}

func (subfs *SubFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []File, err error) {
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

func (subfs *SubFileSystem) OpenReader(filePath string) (fs.File, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	panic("not implemented")
}

func (subfs *SubFileSystem) Watch(filePath string, onEvent func(File, Event)) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Unwatch(filePath string) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) Truncate(filePath string, size int64) error {
	panic("not implemented")
}

func (subfs *SubFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
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
