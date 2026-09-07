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

func (fs InvalidFileSystem) ID() string {
	return fs.String()
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

func (InvalidFileSystem) Separator() string {
	return "/"
}

func (InvalidFileSystem) ReadableWritable() (readable, writable bool) {
	return false, false
}

func (InvalidFileSystem) RootDir() File {
	return InvalidFile
}

// CleanPath cleans the path without a leading separator,
// because the prefix already ends with a slash (like httpfs).
func (fs InvalidFileSystem) CleanPath(uriParts ...string) string {
	return fsimpl.PathHelper{URIPrefix: fs.Prefix()}.CleanPath(uriParts...)
}

func (InvalidFileSystem) Stat(filePath string) (*FileInfo, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	return nil, ErrInvalidFileSystem
}

func (InvalidFileSystem) MakeDir(dirPath string, perm Permissions) error {
	return ErrInvalidFileSystem
}

func (InvalidFileSystem) Remove(filePath string) error {
	return ErrInvalidFileSystem
}

// Close does nothing and returns nil.
func (InvalidFileSystem) Close() error {
	return nil
}
