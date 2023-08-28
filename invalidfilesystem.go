package fs

import (
	"context"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

var _ fullyFeaturedFileSystem = InvalidFileSystem{}

// InvalidFileSystem is a file system where all operations are invalid.
// A File with an empty path defaults to this FS.
type InvalidFileSystem struct{}

func (InvalidFileSystem) IsReadOnly() bool {
	return true
}

func (InvalidFileSystem) IsWriteOnly() bool {
	return false
}

func (InvalidFileSystem) RootDir() File {
	return ""
}

func (InvalidFileSystem) ID() (string, error) {
	return "invalid file system", nil
}

func (InvalidFileSystem) Prefix() string {
	return "invalid://"
}

func (InvalidFileSystem) Name() string {
	return "invalid file system"
}

func (InvalidFileSystem) String() string {
	return "invalid file system"
}

func (invalid InvalidFileSystem) JoinCleanFile(uri ...string) File {
	return File("invalid://" + invalid.JoinCleanPath(uri...))
}

func (InvalidFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (invalid InvalidFileSystem) AbsPath(filePath string) string {
	return invalid.JoinCleanPath(filePath)
}

func (InvalidFileSystem) URL(cleanPath string) string {
	return "invalid://" + cleanPath
}

func (InvalidFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], "invalid://")
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath)
	return cleanPath
}

func (InvalidFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, "invalid://")
	filePath = strings.Trim(filePath, "/")
	return strings.Split(filePath, "/")
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

func (InvalidFileSystem) Stat(filePath string) (os.FileInfo, error) {
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

func (InvalidFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDirMax(ctx context.Context, dirPath string, n int, patterns []string) (files []File, err error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) User(filePath string) string {
	return ""
}

func (InvalidFileSystem) SetUser(filePath string, user string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Group(filePath string) string {
	return ""
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

func (InvalidFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenReader(filePath string) (fs.File, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
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
