package fs

import (
	"io"
	"time"
)

type File interface {
	FileSystem() FileSystem

	URN() string
	URL() string
	Path() string
	Name() string
	Ext() string

	Exists() bool
	IsDir() bool
	Size() int64

	Watch() (<-chan WatchEvent, error)

	ListDir(callback func(File) error, patterns ...string) error

	ModTime() time.Time

	Permissions() Permissions
	SetPermissions(perm Permissions) error

	User() string
	SetUser(user string) error

	Group() string
	SetGroup(user string) error

	OpenReader() (io.ReadCloser, error)
	OpenWriter() (io.WriteCloser, error)
	OpenAppendWriter() (io.WriteCloser, error)
	OpenReadWriter() (io.ReadWriteCloser, error)
}
