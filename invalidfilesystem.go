package fs

import (
	"context"
	"io"
	"io/fs"
	"net/url"
	"os"
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

func (invalid InvalidFileSystem) Root() File {
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

func (InvalidFileSystem) Separator() string {
	return "/"
}

func (InvalidFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return false, ErrInvalidFileSystem
}

func (InvalidFileSystem) DirAndName(filePath string) (dir, name string) {
	return "", ""
}

func (InvalidFileSystem) VolumeName(filePath string) string {
	return ""
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

func (InvalidFileSystem) IsEmpty(filePath string) bool {
	return true
}

func (InvalidFileSystem) ListDir(dirPath string, listDirs bool, patterns []string, onDirEntry func(fs.DirEntry) error) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDirRecursive(dirPath string, listDirs bool, patterns []string, onDirEntry func(dir string, entry DirEntry) error) error {
	return ErrInvalidFileSystem
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

func (InvalidFileSystem) ReadAll(filePath string) ([]byte, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) WriteAll(filePath string, data []byte, perm []Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Append(filePath string, data []byte, perm []Permissions) error {
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

func (InvalidFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) Truncate(filePath string, size int64) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Rename(filePath string, newName string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Move(filePath string, destPath string) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Remove(filePath string) error {
	return ErrInvalidFileSystem
}
