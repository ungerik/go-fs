package fs

import (
	"context"
	iofs "io/fs"
	"path/filepath"

	"github.com/ungerik/go-fs/fsimpl"
)

// todoSubFileSystemPrefix is the URI prefix used to identify SubFileSystem files
const todoSubFileSystemPrefix = "sub://"

type todoSubFileSystem struct {
	prefix   string
	Parent   FileSystem
	BasePath string
}

func todoNewSubFileSystem(parent FileSystem, basePath string) *todoSubFileSystem {
	subfs := &todoSubFileSystem{
		prefix:   todoSubFileSystemPrefix + fsimpl.RandomString(),
		Parent:   parent,
		BasePath: basePath,
	}
	Register(subfs)
	return subfs
}

func (subfs *todoSubFileSystem) IsReadOnly() bool {
	return subfs.Parent.IsReadOnly()
}

func (subfs *todoSubFileSystem) IsWriteOnly() bool {
	return subfs.Parent.IsWriteOnly()
}

func (subfs *todoSubFileSystem) RootDir() File {
	return File(subfs.Parent.Separator())
}

func (subfs *todoSubFileSystem) ID() (string, error) {
	parentID, err := subfs.Parent.ID()
	if err != nil {
		return "", err
	}
	return parentID + "/" + subfs.BasePath, nil
}

func (subfs *todoSubFileSystem) Prefix() string {
	return subfs.prefix
}

func (subfs *todoSubFileSystem) Name() string {
	return "Sub file system of " + subfs.Parent.Name()
}

// String implements the fmt.Stringer interface.
func (subfs *todoSubFileSystem) String() string {
	return subfs.Name() + " with prefix " + subfs.Prefix()
}

///////////////////////////////////////////////////
// TODO Replace implementation with real SubFileSystem from here on:
///////////////////////////////////////////////////

func (subfs *todoSubFileSystem) JoinCleanFile(uri ...string) File {
	if len(uri) == 0 {
		panic("SubFileSystem uri must not be empty")
	}

	return File(filepath.Clean(filepath.Join(uri...)))
}

func (subfs *todoSubFileSystem) URN(filePath string) string {
	return filepath.ToSlash(filePath)
}

func (subfs *todoSubFileSystem) URL(filePath string) string {
	return LocalPrefix + subfs.URN(filePath)
}

func (subfs *todoSubFileSystem) JoinCleanPath(uri ...string) string {
	return subfs.prefix + subfs.Parent.JoinCleanPath(uri...)
}

func (subfs *todoSubFileSystem) SplitPath(filePath string) []string {
	return subfs.Parent.SplitPath(filePath)
}

func (subfs *todoSubFileSystem) Separator() string {
	return subfs.Parent.Separator()
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (subfs *todoSubFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return subfs.Parent.MatchAnyPattern(name, patterns)
}

func (subfs *todoSubFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) VolumeName(filePath string) string {
	if fs, ok := subfs.Parent.(VolumeNameFileSystem); ok {
		return fs.VolumeName(filePath)
	}
	return ""
}

func (subfs *todoSubFileSystem) IsAbsPath(filePath string) bool {
	return subfs.Parent.IsAbsPath(filePath)
}

func (subfs *todoSubFileSystem) AbsPath(filePath string) string {
	return subfs.Parent.AbsPath(filePath)
}

func (subfs *todoSubFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	return subfs.Parent.Stat(filePath)
}

func (subfs *todoSubFileSystem) Exists(filePath string) bool {
	if fs, ok := subfs.Parent.(ExistsFileSystem); ok {
		return fs.Exists(filePath)
	}
	_, err := subfs.Parent.Stat(filePath)
	return err != nil
}

func (subfs *todoSubFileSystem) IsHidden(filePath string) bool {
	return subfs.Parent.IsHidden(filePath)
}

func (subfs *todoSubFileSystem) IsSymbolicLink(filePath string) bool {
	return subfs.Parent.IsSymbolicLink(filePath)
}

func (subfs *todoSubFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []File, err error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) User(filePath string) (string, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) SetUser(filePath string, user string) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) Group(filePath string) (string, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) SetGroup(filePath string, group string) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) Touch(filePath string, perm []Permissions) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) MakeDir(filePath string, perm []Permissions) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) OpenReader(filePath string) (ReadCloser, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	panic("TODO")
}

func (subfs *todoSubFileSystem) Truncate(filePath string, size int64) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) Remove(filePath string) error {
	panic("TODO")
}

func (subfs *todoSubFileSystem) Close() error {
	panic("TODO")
}
