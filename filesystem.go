package fs

import (
	"context"
	"io"
	iofs "io/fs"
)

type (
	ReadCloser  = iofs.File
	WriteCloser = io.WriteCloser
)

// FileSystem is an interface that has to be implemented for
// a file system to be accessable via this package.
type FileSystem interface {
	ReadableWritable() (readable, writable bool)

	// RootDir returns the file system root directory
	RootDir() File

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

	// Separator for paths of the file system
	Separator() string

	// IsAbsPath indicates if the passed filePath is absolute
	IsAbsPath(filePath string) bool

	// AbsPath returns the passe filePath in absolute form
	AbsPath(filePath string) string

	// MatchAnyPattern returns true if name matches any of patterns,
	// or if len(patterns) == 0.
	// The match per pattern works like path.Match or filepath.Match
	MatchAnyPattern(name string, patterns []string) (bool, error)

	// SplitDirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
	// If filePath is the root of the file systeme, then an empty string will be returned for name.
	SplitDirAndName(filePath string) (dir, name string)

	Stat(filePath string) (iofs.FileInfo, error)

	// IsHidden returns if a file is hidden depending
	// on the definition of hidden files of the file system,
	// but it will always return true if the name of the file starts with a dot.
	IsHidden(filePath string) bool // TODO

	// IsSymbolicLink returns if a file is a symbolic link
	IsSymbolicLink(filePath string) bool // TODO

	// ListDirInfo calls the passed callback function for every file and directory in dirPath.
	// If any patterns are passed, then only files or directores with a name that matches
	// at least one of the patterns are returned.
	ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error

	MakeDir(dirPath string, perm []Permissions) error

	OpenReader(filePath string) (ReadCloser, error)
	OpenWriter(filePath string, perm []Permissions) (WriteCloser, error)
	OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)

	// Remove deletes the file.
	Remove(filePath string) error

	// Close the file system or do nothing if it is not closable
	Close() error
}

type fullyFeaturedFileSystem interface {
	FileSystem
	CopyFileSystem
	MoveFileSystem
	RenameFileSystem
	VolumeNameFileSystem
	WatchFileSystem
	TouchFileSystem
	MakeAllDirsFileSystem
	ReadAllFileSystem
	WriteAllFileSystem
	AppendFileSystem
	AppendWriterFileSystem
	TruncateFileSystem
	ExistsFileSystem
	UserFileSystem
	GroupFileSystem
	PermissionsFileSystem
}

// CopyFileSystem can be implemented by file systems
// that have native file copying functionality.
//
// If a file system does not implement this interface
// then it's functionality will be emulated with
// other methods.
type CopyFileSystem interface {
	FileSystem

	// CopyFile copies a single file.
	// buf must point to a []byte variable.
	// If that variable is initialized with a byte slice, then this slice will be used as buffer,
	// else a byte slice will be allocated for the variable.
	CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error
}

// MoveFileSystem can be implemented by file systems
// that have native file moving functionality.
//
// If a file system does not implement this interface
// then it's functionality will be emulated with
// other methods.
type MoveFileSystem interface {
	FileSystem

	// Move moves and/or renames the file to destination.
	// destination can be a directory or file-path.
	// If successful, this also changes the path of this File's implementation.
	Move(filePath string, destinationPath string) error
}

// RenameFileSystem can be implemented by file systems
// that have native file renaming functionality.
//
// If a file system does not implement this interface
// then it's functionality will be emulated with
// other methods.
type RenameFileSystem interface {
	FileSystem

	// Rename only renames the file in its base directory
	// but does not move it into another directory.
	// If successful, this also changes the path of this File's implementation.
	Rename(filePath string, newName string) (newPath string, err error)
}

// VolumeNameFileSystem should be implemented by file systems
// that have volume names.
type VolumeNameFileSystem interface {
	FileSystem
	// VolumeName returns the name of the volume at the beginning of the filePath,
	// or an empty string if the filePath has no volume.
	// A volume is for example "C:" on Windows
	VolumeName(filePath string) string
}

// WatchFileSystem can be implemented by file systems
// that have file watching functionality.
type WatchFileSystem interface {
	FileSystem

	// Watch a file or directory for changes.
	// If filePath describes a directory then
	// changes directly within it will be reported.
	// This does not apply changes in deeper
	// recursive sub-directories.
	//
	// It is valid to watch a file with multiple
	// callbacks, calling the returned cancel function
	// will cancel a particular watch.
	Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error)
}

type TouchFileSystem interface {
	FileSystem

	Touch(filePath string, perm []Permissions) error
}

type ReadAllFileSystem interface {
	FileSystem

	ReadAll(ctx context.Context, filePath string) ([]byte, error)
}

type WriteAllFileSystem interface {
	FileSystem

	WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error
}

type AppendFileSystem interface {
	FileSystem

	Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error
}

type AppendWriterFileSystem interface {
	FileSystem

	OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error)
}

type TruncateFileSystem interface {
	FileSystem

	// Truncate resizes a file by not only
	// truncating to a smaller size but also
	// appending zeros to a bigger size
	// than the current one.
	Truncate(filePath string, size int64) error
}

type ExistsFileSystem interface {
	FileSystem

	// Exists returns if a file exists and is accessible.
	// Depending on the FileSystem implementation,
	// this could be faster than using Stat.
	// Note that a file could exist but might not be accessible.
	Exists(filePath string) bool
}

type UserFileSystem interface {
	FileSystem

	User(filePath string) (string, error)
	SetUser(filePath string, user string) error
}

type GroupFileSystem interface {
	FileSystem

	Group(filePath string) (string, error)
	SetGroup(filePath string, group string) error
}

type PermissionsFileSystem interface {
	FileSystem

	SetPermissions(filePath string, perm Permissions) error
}

type MakeAllDirsFileSystem interface {
	FileSystem

	MakeAllDirs(dirPath string, perm []Permissions) error
}

type ListDirMaxFileSystem interface {
	// ListDirMax returns at most max files and directories in dirPath.
	// A max value of -1 returns all files.
	// If any patterns are passed, then only files or directories with a name that matches
	// at least one of the patterns are returned.
	ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error)
}

type ListDirRecursiveFileSystem interface {
	// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
	// recursing into all sub-directories.
	// If any patterns are passed, then only files (not directories) with a name that matches
	// at least one of the patterns are returned.
	ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error
}
