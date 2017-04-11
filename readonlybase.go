package fs

import (
	"errors"
	"io"
)

// ReadOnlyBase implements the writing methods of the FileSystem interface
// to do nothing and return ErrReadOnlyFileSystem.
// Intended to be used as base for read only file systems,
// so that only the read methods have to be implemented.
type ReadOnlyBase struct {
}

func (*ReadOnlyBase) IsReadOnly() bool {
	return true
}

func (*ReadOnlyBase) SetPermissions(filePath string, perm Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) SetUser(filePath string, user string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) SetGroup(filePath string, group string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Touch(filePath string, perm ...Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) MakeDir(filePath string, perm ...Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) WriteAll(filePath string, data []byte, perm ...Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Append(filePath string, data []byte, perm ...Permissions) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenWriter(filePath string, perm ...Permissions) (WriteSeekCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenAppendWriter(filePath string, perm ...Permissions) (io.WriteCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) OpenReadWriter(filePath string, perm ...Permissions) (ReadWriteSeekCloser, error) {
	return nil, ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, errors.New("not implemented")
}

func (*ReadOnlyBase) Truncate(filePath string, size int64) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Rename(filePath string, newName string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Move(filePath string, destPath string) error {
	return ErrReadOnlyFileSystem
}

func (*ReadOnlyBase) Remove(filePath string) error {
	return ErrReadOnlyFileSystem
}
