package fs

import (
	"context"
	"io"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ FileSystem      = InvalidFileSystem("")
	_ WriteFileSystem = InvalidFileSystem("")
)

// InvalidFileSystem is a file system where all operations are invalid.
// A File with an empty path defaults to this FS.
//
// The underlying string value is the optional name
// of the file system and will be added to the URI prefix.
// It can be used to register different dummy file systems
// for debugging or testing purposes.
type InvalidFileSystem string

// ID returns the same string as String.
func (fs InvalidFileSystem) ID() string {
	return fs.String()
}

// Prefix returns "invalid://", extended with the name
// of the file system if the underlying string is not empty.
func (fs InvalidFileSystem) Prefix() string {
	if fs == "" {
		return "invalid://"
	}
	return "invalid://" + strings.Trim(string(fs), "/") + "/"
}

// Name returns the underlying string of the file system
// or "invalid file system" if it is empty.
func (fs InvalidFileSystem) Name() string {
	if fs == "" {
		return "invalid file system"
	}
	return string(fs)
}

// String returns "invalid file system", followed by
// the underlying string if it is not empty.
func (fs InvalidFileSystem) String() string {
	if fs == "" {
		return "invalid file system"
	}
	return "invalid file system" + " " + string(fs)
}

// Separator returns "/".
func (InvalidFileSystem) Separator() string {
	return "/"
}

// ReadableWritable returns false, false because no operation is possible.
func (InvalidFileSystem) ReadableWritable() (readable, writable bool) {
	return false, false
}

// RootDir returns InvalidFile.
func (InvalidFileSystem) RootDir() File {
	return InvalidFile
}

// CleanPath cleans the path without a leading separator,
// because the prefix already ends with a slash (like httpfs).
func (fs InvalidFileSystem) CleanPath(uriParts ...string) string {
	return fsimpl.PathHelper{URIPrefix: fs.Prefix()}.CleanPath(uriParts...)
}

// Stat returns ErrInvalidFileSystem.
func (InvalidFileSystem) Stat(filePath string) (*FileInfo, error) {
	return nil, ErrInvalidFileSystem
}

// ListDir returns ErrInvalidFileSystem.
func (InvalidFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	return ErrInvalidFileSystem
}

// OpenReader returns ErrInvalidFileSystem.
func (InvalidFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return nil, ErrInvalidFileSystem
}

// OpenWriter returns ErrInvalidFileSystem.
func (InvalidFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

// MakeDir returns ErrInvalidFileSystem.
func (InvalidFileSystem) MakeDir(dirPath string, perm Permissions) error {
	return ErrInvalidFileSystem
}

// Remove returns ErrInvalidFileSystem.
func (InvalidFileSystem) Remove(filePath string) error {
	return ErrInvalidFileSystem
}

// Close does nothing and returns nil.
func (InvalidFileSystem) Close() error {
	return nil
}
