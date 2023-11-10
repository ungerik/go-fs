package fs

import (
	"context"
	iofs "io/fs"
	"net/url"
	"path"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

var _ fullyFeaturedFileSystem = InvalidFileSystem("")

// InvalidFileSystem is a file system where all operations are invalid.
// A File with an empty path defaults to this FS.
//
// The underlying string value is the optional name
// of the file system and will be added to the URI prefix.
// It can be used to register different dummy file systems
// for debugging or testing purposes.
type InvalidFileSystem string

func (InvalidFileSystem) IsReadOnly() bool {
	return true
}

func (InvalidFileSystem) IsWriteOnly() bool {
	return false
}

func (InvalidFileSystem) RootDir() File {
	return ""
}

func (fs InvalidFileSystem) ID() (string, error) {
	return fs.String(), nil
}

func (fs InvalidFileSystem) Prefix() string {
	if fs == "" {
		return "invalid://"
	}
	return "invalid://" + strings.Trim(string(fs), "/") + "/"
}

func (fs InvalidFileSystem) Name() string {
	if fs == "" {
		return "invalid file system"
	}
	return string(fs)
}

func (fs InvalidFileSystem) String() string {
	if fs == "" {
		return "invalid file system"
	}
	return "invalid file system" + " " + string(fs)
}

func (fs InvalidFileSystem) JoinCleanFile(uri ...string) File {
	return File(fs.Prefix() + fs.JoinCleanPath(uri...))
}

func (InvalidFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (fs InvalidFileSystem) AbsPath(filePath string) string {
	return fs.JoinCleanPath(filePath)
}

func (fs InvalidFileSystem) URL(cleanPath string) string {
	return fs.Prefix() + cleanPath
}

func (fs InvalidFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], fs.Prefix())
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath)
	return cleanPath
}

func (fs InvalidFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, fs.Prefix(), fs.Separator())
}

func (InvalidFileSystem) Separator() string {
	return "/"
}

func (InvalidFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return false, ErrInvalidFileSystem
}

func (InvalidFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, "/")
}

func (InvalidFileSystem) VolumeName(filePath string) string {
	return "invalid:"
}

func (InvalidFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) Exists(filePath string) bool {
	return false
}

func (InvalidFileSystem) IsHidden(filePath string) bool {
	return false
}

func (InvalidFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (InvalidFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDirMax(ctx context.Context, dirPath string, n int, patterns []string) (files []File, err error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) User(filePath string) (string, error) {
	return "", ErrInvalidFileSystem
}

func (InvalidFileSystem) SetUser(filePath string, user string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Group(filePath string) (string, error) {
	return "", ErrInvalidFileSystem
}

func (InvalidFileSystem) SetGroup(filePath string, group string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Touch(filePath string, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) MakeAllDirs(dirPath string, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenReader(filePath string) (ReadCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) Truncate(filePath string, size int64) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Rename(filePath string, newName string) (string, error) {
	return "", ErrInvalidFileSystem
}

func (InvalidFileSystem) Move(filePath string, destPath string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Remove(filePath string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Close() error {
	return ErrInvalidFileSystem
}
