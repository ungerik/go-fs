package fs

import (
	"io"
	"time"
)

// FileInfo is returned by FileSystem.Stat()
type FileInfo struct {
	Exists      bool
	IsDir       bool
	Size        int64
	ModTime     time.Time
	Permissions Permissions
}

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

	// Stat returns FileInfo
	Stat(filePath string) FileInfo

	Watch(filePath string) (<-chan WatchEvent, error)

	ListDir(dirPath string, callback func(File) error, patterns []string) error

	// ListDirMax: n == -1 lists all
	ListDirMax(dirPath string, max int, patterns []string) ([]File, error)

	SetPermissions(filePath string, perm Permissions) error

	User(filePath string) string
	SetUser(filePath string, user string) error

	Group(filePath string) string
	SetGroup(filePath string, group string) error

	Touch(filePath string, perm []Permissions) error
	MakeDir(dirPath string, perm []Permissions) error

	ReadAll(filePath string) ([]byte, error)
	WriteAll(filePath string, data []byte, perm []Permissions) error
	Append(filePath string, data []byte, perm []Permissions) error

	OpenReader(filePath string) (ReadSeekCloser, error)
	OpenWriter(filePath string, perm []Permissions) (WriteSeekCloser, error)
	OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error)
	OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)

	Truncate(filePath string, size int64) error

	// CopyFile copies a single file.
	// buf must point to a []byte variable.
	// If that variable is initialized with a byte slice, then this slice will be used as buffer,
	// else a byte slice will be allocated for the variable.
	CopyFile(srcFile string, destFile string, buf *[]byte) error

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
