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
// a file system to be accessible via this package.
type FileSystem interface {
	// ReadableWritable returns whether the file system is readable and/or writable.
	ReadableWritable() (readable, writable bool)

	// RootDir returns the file system root directory.
	RootDir() File

	// ID returns a unique identifier for the FileSystem.
	ID() (string, error)

	// Prefix returns the URI prefix for this file system (e.g., "file://", "sftp://").
	Prefix() string

	// Name returns the name of the FileSystem implementation.
	Name() string

	// String returns a descriptive string for the FileSystem implementation.
	String() string

	// URL returns a full URL which is Prefix() + cleanPath.
	// Note that the passed cleanPath will not be cleaned
	// by the FileSystem implementation.
	URL(cleanPath string) string

	// CleanPathFromURI returns the clean path part of a URI
	// specific to the implementation of the FileSystem.
	// It's the inverse of the URL method.
	CleanPathFromURI(uri string) string

	// JoinCleanFile joins the file system prefix with uriParts
	// into a File with clean path and prefix.
	JoinCleanFile(uriParts ...string) File

	// JoinCleanPath joins the uriParts into a cleaned path
	// of the file system style without the file system prefix.
	JoinCleanPath(uriParts ...string) string

	// SplitPath returns all Separator() delimited components of filePath
	// without the file system prefix.
	SplitPath(filePath string) []string

	// Separator returns the path separator for the file system.
	Separator() string

	// IsAbsPath indicates if the passed filePath is absolute.
	// For local file systems, this checks if the path starts with / or a volume name.
	// For remote file systems, this may check if the path contains the file system prefix.
	IsAbsPath(filePath string) bool

	// AbsPath returns the passed filePath in absolute form.
	// For relative paths, this converts them to absolute paths.
	// If the absolute path cannot be determined, returns the original filePath.
	AbsPath(filePath string) string

	// MatchAnyPattern returns true if name matches any of patterns,
	// or if len(patterns) == 0.
	// The match per pattern works like path.Match or filepath.Match.
	MatchAnyPattern(name string, patterns []string) (bool, error)

	// SplitDirAndName returns the parent directory of filePath and the name within that directory of the last filePath element.
	// If filePath is the root of the file system, then an empty string will be returned for name.
	SplitDirAndName(filePath string) (dir, name string)

	// Stat returns file information for the file or directory at filePath.
	// Returns an error if the file does not exist or is not accessible.
	Stat(filePath string) (iofs.FileInfo, error)

	// IsHidden returns if a file or directory is hidden depending
	// on the definition of hidden files of the file system.
	// Files and directories starting with a dot are typically considered hidden.
	IsHidden(filePath string) bool

	// IsSymbolicLink returns if a file or directory is a symbolic link.
	// Not all file systems support symbolic links.
	IsSymbolicLink(filePath string) bool

	// ListDirInfo calls the passed callback function for every file and directory in dirPath.
	// If any patterns are passed, then only files or directories with a name that matches
	// at least one of the patterns are returned.
	// Canceling the context or returning an error from the callback
	// will stop the listing and return the context or callback error.
	// Does not recurse into subdirectories.
	ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error

	// MakeDir creates a single directory at dirPath.
	// Does not create parent directories if they don't exist.
	// Use MakeAllDirs for recursive directory creation.
	MakeDir(dirPath string, perm []Permissions) error

	// OpenReader opens the file at filePath for reading.
	OpenReader(filePath string) (ReadCloser, error)

	// OpenWriter opens the file at filePath for writing, creating it if it does not exist
	// or truncating it if it does exist.
	OpenWriter(filePath string, perm []Permissions) (WriteCloser, error)

	// OpenReadWriter opens the file at filePath for reading and writing,
	// creating it if it does not exist but not truncating it if it does exist.
	OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)

	// Remove deletes the file or empty directory at filePath.
	// For non-empty directories, behavior is implementation-specific.
	// Use RemoveRecursive on File for recursive deletion.
	Remove(filePath string) error

	// Close closes the file system or does nothing if it is not closable.
	Close() error
}

// FullyFeaturedFileSystem is an interface that extends the FileSystem interface
// with additional methods for all optionally optimized file system operations.
type FullyFeaturedFileSystem interface {
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

// TouchFileSystem can be implemented by file systems
// that have native touch functionality.
type TouchFileSystem interface {
	FileSystem

	// Touch creates an empty file at filePath if it does not exist,
	// or updates the modification time if it does exist.
	Touch(filePath string, perm []Permissions) error
}

// ReadAllFileSystem can be implemented by file systems
// that can read entire files more efficiently than using OpenReader.
type ReadAllFileSystem interface {
	FileSystem

	// ReadAll reads the entire content of the file at filePath.
	ReadAll(ctx context.Context, filePath string) ([]byte, error)
}

// WriteAllFileSystem can be implemented by file systems
// that can write entire files more efficiently than using OpenWriter.
type WriteAllFileSystem interface {
	FileSystem

	// WriteAll writes data to the file at filePath, creating it if it does not exist
	// or truncating it if it does exist.
	WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error
}

// AppendFileSystem can be implemented by file systems
// that can append to files more efficiently than using OpenAppendWriter.
type AppendFileSystem interface {
	FileSystem

	// Append appends data to the file at filePath, creating it if it does not exist.
	Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error
}

// AppendWriterFileSystem can be implemented by file systems
// that have native append writer functionality.
type AppendWriterFileSystem interface {
	FileSystem

	// OpenAppendWriter opens the file at filePath for appending,
	// creating it if it does not exist.
	OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error)
}

// TruncateFileSystem can be implemented by file systems
// that have native file truncation/resizing functionality.
type TruncateFileSystem interface {
	FileSystem

	// Truncate resizes a file by not only
	// truncating to a smaller size but also
	// appending zeros to a bigger size
	// than the current one.
	Truncate(filePath string, size int64) error
}

// ExistsFileSystem can be implemented by file systems
// that can check file existence more efficiently than using Stat.
type ExistsFileSystem interface {
	FileSystem

	// Exists returns if a file exists and is accessible.
	// Depending on the FileSystem implementation,
	// this could be faster than using Stat.
	// Note that a file could exist but might not be accessible.
	Exists(filePath string) bool
}

// UserFileSystem can be implemented by file systems
// that support file ownership operations.
type UserFileSystem interface {
	FileSystem

	// User returns the user owner of the file at filePath.
	User(filePath string) (string, error)

	// SetUser sets the user owner of the file at filePath.
	SetUser(filePath string, user string) error
}

// GroupFileSystem can be implemented by file systems
// that support file group operations.
type GroupFileSystem interface {
	FileSystem

	// Group returns the group owner of the file at filePath.
	Group(filePath string) (string, error)

	// SetGroup sets the group owner of the file at filePath.
	SetGroup(filePath string, group string) error
}

// PermissionsFileSystem can be implemented by file systems
// that support setting file permissions.
type PermissionsFileSystem interface {
	FileSystem

	// SetPermissions sets the permissions of the file at filePath.
	SetPermissions(filePath string, perm Permissions) error
}

// MakeAllDirsFileSystem can be implemented by file systems
// that have native recursive directory creation functionality.
//
// If a file system does not implement this interface
// then its functionality will be emulated with
// other methods.
type MakeAllDirsFileSystem interface {
	FileSystem

	// MakeAllDirs creates all directories in dirPath recursively,
	// similar to mkdir -p.
	MakeAllDirs(dirPath string, perm []Permissions) error
}

// ListDirMaxFileSystem can be implemented by file systems
// that can efficiently list a limited number of directory entries.
type ListDirMaxFileSystem interface {
	FileSystem

	// ListDirMax returns at most max files and directories in dirPath.
	// A max value of -1 returns all files.
	// If any patterns are passed, then only files or directories with a name that matches
	// at least one of the patterns are returned.
	ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error)
}

// ListDirRecursiveFileSystem can be implemented by file systems
// that can efficiently list directory entries recursively.
type ListDirRecursiveFileSystem interface {
	FileSystem

	// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
	// recursing into all sub-directories.
	// If any patterns are passed, then only files (not directories) with a name that matches
	// at least one of the patterns are returned.
	ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error
}

// XAttrFileSystem extends FileSystem with support for extended file attributes (xattrs).
// Extended attributes are name-value pairs associated with files that provide
// additional metadata beyond standard file attributes.
// Not all file systems support xattrs (e.g., FAT32 does not).
// On Linux, xattr names are typically prefixed with a namespace like "user.".
type XAttrFileSystem interface {
	FileSystem

	// ListXAttr returns the names of all extended attributes for the file.
	// If followSymlinks is true, symlinks are resolved to their target.
	ListXAttr(filePath string, followSymlinks bool) ([]string, error)

	// GetXAttr returns the value of the named extended attribute.
	// If followSymlinks is true, symlinks are resolved to their target.
	GetXAttr(filePath string, name string, followSymlinks bool) ([]byte, error)

	// SetXAttr sets the value of the named extended attribute.
	// The flags parameter controls the behavior (e.g., unix.XATTR_CREATE, unix.XATTR_REPLACE).
	// If followSymlinks is true, symlinks are resolved to their target.
	SetXAttr(filePath string, name string, data []byte, flags int, followSymlinks bool) error

	// RemoveXAttr removes the named extended attribute from the file.
	// If followSymlinks is true, symlinks are resolved to their target.
	RemoveXAttr(filePath string, name string, followSymlinks bool) error
}
