package fs

import (
	"context"
	"io"
)

// FileSystem is an interface that has to be implemented for
// a file system to be accessable via this package.
type FileSystem interface {
	IsReadOnly() bool
	IsWriteOnly() bool

	Root() File

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

	// DirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
	// If filePath is the root of the file systeme, then an empty string will be returned for name.
	DirAndName(filePath string) (dir, name string)

	// VolumeName returns the name of the volume at the beginning of the filePath,
	// or an empty string if the filePath has no volume.
	// A volume is for example "C:" on Windows
	VolumeName(filePath string) string

	// Info returns FileInfo
	Info(filePath string) FileInfo
	IsHidden(filePath string) bool
	IsSymbolicLink(filePath string) bool

	Watch(filePath string) (<-chan WatchEvent, error)

	// ListDirInfo calls the passed callback function for every file and directory in dirPath.
	// If any patterns are passed, then only files or directores with a name that matches
	// at least one of the patterns are returned.
	ListDirInfo(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error

	// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
	// recursing into all sub-directories.
	// If any patterns are passed, then only files (not directories) with a name that matches
	// at least one of the patterns are returned.
	ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error

	// ListDirMax returns at most max files and directories in dirPath.
	// A max value of -1 returns all files.
	// If any patterns are passed, then only files or directories with a name that matches
	// at least one of the patterns are returned.
	ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error)

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

	OpenReader(filePath string) (io.ReadCloser, error)
	OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error)
	OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error)
	OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)

	Truncate(filePath string, size int64) error

	// CopyFile copies a single file.
	// buf must point to a []byte variable.
	// If that variable is initialized with a byte slice, then this slice will be used as buffer,
	// else a byte slice will be allocated for the variable.
	CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error

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
// Calling its Close method will remove it from the Registry
// and undo other initializations.
type TransientFileSystem interface {
	FileSystem
	Close() error
}
