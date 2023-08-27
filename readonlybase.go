package fs

import (
	"context"
	"io"

	"github.com/ungerik/go-fs/fsimpl"
)

// ReadOnlyBase implements the writing methods of the FileSystem interface
// to do nothing and return ErrReadOnlyFileSystem.
// Intended to be used as base for read only file systems,
// so that only the read methods have to be implemented.
type ReadOnlyBase struct{}

func (*ReadOnlyBase) IsReadOnly() bool {
	return true
}

func (*ReadOnlyBase) IsWriteOnly() bool {
	return false
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*ReadOnlyBase) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (*ReadOnlyBase) SetPermissions(filePath string, perm Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) User(filePath string) string { return "" }

func (*ReadOnlyBase) SetUser(filePath string, user string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Group(filePath string) string { return "" }

func (*ReadOnlyBase) SetGroup(filePath string, group string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Touch(filePath string, perm []Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) MakeDir(dirPath string, perm []Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Truncate(filePath string, size int64) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Remove(filePath string) error {
	return ErrReadOnlyFileSystem
}
