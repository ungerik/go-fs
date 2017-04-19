package fs

import (
	"io"
	"time"
)

// FileSystem is an interface that has to be implemented for
// a file system to be accessable via this package.
type FileSystem interface {
	IsReadOnly() bool
	Prefix() string
	Name() string

	File(uriParts ...string) File

	// URL returns a full URL wich is Prefix() + cleanPath
	URL(cleanPath string) string

	// CleanPath joins the uriParts into a cleaned path
	// of the file system style without the file system prefix
	CleanPath(uriParts ...string) string

	// SplitPath returns all Seperator() delimited components of filePath
	// without the file system prefix.
	SplitPath(filePath string) []string

	Seperator() string

	FileName(filePath string) string

	Ext(filePath string) string
	Dir(filePath string) string

	Exists(filePath string) bool
	IsDir(filePath string) bool

	// Size returns the size of the file with filePath or 0 if it does not exist or is a directory.
	Size(filePath string) int64

	Watch(filePath string) (<-chan WatchEvent, error)

	ListDir(dirPath string, callback func(File) error, patterns []string) error

	// ListDirMax: n == -1 lists all
	ListDirMax(dirPath string, n int, patterns []string) ([]File, error)

	ModTime(filePath string) time.Time

	Permissions(filePath string) Permissions
	SetPermissions(filePath string, perm Permissions) error

	User(filePath string) string
	SetUser(filePath string, user string) error

	Group(filePath string) string
	SetGroup(filePath string, group string) error

	Touch(filePath string, perm []Permissions) error
	MakeDir(filePath string, perm []Permissions) error

	ReadAll(filePath string) ([]byte, error)
	WriteAll(filePath string, data []byte, perm []Permissions) error
	Append(filePath string, data []byte, perm []Permissions) error

	OpenReader(filePath string) (ReadSeekCloser, error)
	OpenWriter(filePath string, perm []Permissions) (WriteSeekCloser, error)
	OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error)
	OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)

	Truncate(filePath string, size int64) error

	// Rename only renames the file in its base directory
	// but does not move it into another directory.
	// If successful, this also changes the path of this File's implementation.
	Rename(filePath string, newName string) error

	// Move moves and/or renames the file to destination.
	// destination can be a directory or file-path.
	// If successful, this also changes the path of this File's implementation.
	Move(filePath string, destinationPath string) error

	// Remove deletes the file.
	Remove(filePath string) error
}
