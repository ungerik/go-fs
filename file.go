package fs

import (
	"io"
	"time"
)

type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

type WriteSeekCloser interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

type ReadWriteSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

type File interface {
	FileSystem() FileSystem

	String() string

	URN() string
	URL() string
	Path() string
	Dir() string
	Name() string
	Ext() string

	Exists() bool
	IsDir() bool
	Size() int64

	Watch() (<-chan WatchEvent, error)

	ListDir(callback func(File) error, patterns ...string) error

	// ListDirMax: n == -1 lists all
	ListDirMax(n int, patterns ...string) ([]File, error)

	ModTime() time.Time

	Permissions() Permissions
	SetPermissions(perm Permissions) error

	User() string
	SetUser(user string) error

	Group() string
	SetGroup(user string) error

	Touch(perm ...Permissions) error
	MakeDir(perm ...Permissions) error

	ReadAll() ([]byte, error)
	WriteAll(data []byte, perm ...Permissions) error
	Append(data []byte, perm ...Permissions) error

	OpenReader() (ReadSeekCloser, error)
	OpenWriter(perm ...Permissions) (WriteSeekCloser, error)
	OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error)
	OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error)

	Truncate(size int64) error

	// Rename only renames the file in its base directory
	// but does not move it into another directory.
	// If successful, this also changes the path of this File's implementation.
	Rename(newName string) error

	// Move moves and/or renames the file to destination.
	// destination can be a directory or file-path.
	// If successful, this also changes the path of this File's implementation.
	Move(destination File) error

	// Remove deletes the file.
	Remove() error
}
