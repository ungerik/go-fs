package fs

import (
	"context"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

// InvalidFileSystem is a file system where all operations are invalid.
// A File with an empty path defaults to this FS.
type InvalidFileSystem struct{}

func (invalid InvalidFileSystem) IsReadOnly() bool {
	return true
}

func (invalid InvalidFileSystem) IsWriteOnly() bool {
	return false
}

func (invalid InvalidFileSystem) RootDir() File {
	return ""
}

func (invalid InvalidFileSystem) ID() (string, error) {
	return "invalid file system", nil
}

func (invalid InvalidFileSystem) Prefix() string {
	return "invalid://"
}

func (invalid InvalidFileSystem) Name() string {
	return "invalid file system"
}

// String implements the fmt.Stringer interface.
func (invalid InvalidFileSystem) String() string {
	return "invalid file system"
}

func (invalid InvalidFileSystem) JoinCleanFile(uri ...string) File {
	return File(invalid.Prefix() + invalid.JoinCleanPath(uri...))
}

func (invalid InvalidFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (invalid InvalidFileSystem) AbsPath(filePath string) string {
	return invalid.JoinCleanPath(filePath)
}

func (invalid InvalidFileSystem) URL(cleanPath string) string {
	return invalid.Prefix() + cleanPath
}

func (invalid InvalidFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], invalid.Prefix())
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath)
	return cleanPath
}

func (invalid InvalidFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, invalid.Prefix())
	filePath = strings.TrimPrefix(filePath, "/")
	filePath = strings.TrimSuffix(filePath, "/")
	return strings.Split(filePath, "/")
}

func (invalid InvalidFileSystem) Separator() string {
	return "/"
}

func (invalid InvalidFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return false, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return "", ""
}

func (InvalidFileSystem) Stat(filePath string) (fs.FileInfo, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) Exists(filePath string) bool {
	return false
}

func (invalid InvalidFileSystem) IsHidden(filePath string) bool {
	return false
}

func (invalid InvalidFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (invalid InvalidFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) ListDirMax(ctx context.Context, dirPath string, n int, patterns []string) (files []File, err error) {
	return nil, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) User(filePath string) string {
	return ""
}

func (invalid InvalidFileSystem) SetUser(filePath string, user string) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) Group(filePath string) string {
	return ""
}

func (invalid InvalidFileSystem) SetGroup(filePath string, group string) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) OpenReader(filePath string) (fs.File, error) {
	return nil, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) Truncate(filePath string, size int64) error {
	return ErrInvalidFileSystem
}

func (invalid InvalidFileSystem) Remove(filePath string) error {
	return ErrInvalidFileSystem
}
