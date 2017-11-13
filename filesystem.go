package fs

import (
	"io"
)

// FileSystem is an interface that has to be implemented for
// a file system to be accessable via this package.
type FileSystem interface {
	IsReadOnly() bool

	// ID returns a unique identifyer for the FileSystem
	ID() (string, error)

	Prefix() string

	// Name returns the name of the FileSystem implementation
	Name() string

	// String returns a descriptive string for the FileSystem implementation
	String() string

	// URL returns a full URL wich is Prefix() + cleanPath
	URL(cleanPath string) string

	// JoinCleanFile joins the file system prefix with uriParts
	// into a File with clean path and prefix
	JoinCleanFile(uriParts ...string) File

	// JoinCleanPath joins the uriParts into a cleaned path
	// of the file system style without the file system prefix
	JoinCleanPath(uriParts ...string) string

	// SplitPath returns all Separator() delimited components of filePath
	// without the file system prefix.
	SplitPath(filePath string) []string

	Separator() string

	IsAbsPath(filePath string) bool
	AbsPath(filePath string) string

	// MatchAnyPattern returns true if name matches any of patterns,
	// or if len(patterns) == 0.
	// The match per pattern works like path.Match or filepath.Match
	MatchAnyPattern(name string, patterns []string) (bool, error)

	DirAndName(filePath string) (dir, name string)

	// Stat returns FileInfo
	Stat(filePath string) FileInfo
	IsHidden(filePath string) bool
	IsSymbolicLink(filePath string) bool

	Watch(filePath string) (<-chan WatchEvent, error)

	ListDirInfo(dirPath string, callback func(File, FileInfo) error, patterns []string) error

	// ListDirInfoRecursive blah.
	// patterns are only applied to files, not to directories
	ListDirInfoRecursive(dirPath string, callback func(File, FileInfo) error, patterns []string) error

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

// TransientFileSystem is a file system that is created
// to wrap a transient data source.
// Calling its Destroy method will remove it from the Registry
// and undo other initializations.
type TransientFileSystem interface {
	FileSystem
	Destroy() error
}
