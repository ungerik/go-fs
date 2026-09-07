package fs

import (
	"context"
	"io"
	iofs "io/fs"
)

type (
	// ReadCloser is the reader type returned by [FileReader.OpenReader]
	// and [File.OpenReader]: an io.ReadCloser with a Stat method.
	ReadCloser = iofs.File

	// WriteCloser is the writer type returned by the open writer methods.
	WriteCloser = io.WriteCloser
)

// FileSystem is the interface that has to be implemented for
// a file system to be accessible via this package.
//
// It consists of the primitives only: identification, path cleaning,
// stat, directory listing and reading. Writing is added by
// [WriteFileSystem], everything else by the optional interfaces below
// which the package emulates with the primitives where it is possible.
//
// File system paths are the output of [FileSystem.CleanPath]:
// cleaned, using the [FileSystem.Separator], without the [FileSystem.Prefix],
// and absolute for the file system (starting with the separator or a volume)
// with the exception of file systems where the path starts with a host name.
// Prefix()+path is the URI of a path. The package guarantees that
// implementations are called with such clean paths and never with the prefix.
//
// Implementations must return errors that satisfy [errors.Is] for
// [os.ErrNotExist] when a file does not exist, [os.ErrExist] when
// [WriteFileSystem.MakeDir] finds an existing path, [ErrIsDirectory] and
// [ErrIsNotDirectory] where applicable, [ErrFileSystemClosed] after [FileSystem.Close],
// and the error of a canceled context. Read-only and write-only file systems
// don't need to implement any checks, the package returns [ErrReadOnlyFileSystem]
// and [ErrWriteOnlyFileSystem] based on [FileSystem.ReadableWritable].
type FileSystem interface {
	// ID returns a string that identifies this file system
	// uniquely among the registered file systems and stays
	// the same for its whole lifetime. It is computed at
	// construction time and never blocks.
	//
	// Where the backing store has an identifier of its own, that
	// one is used: [LocalFileSystem.ID] is the file system id of
	// the root volume, dropboxfs uses the Dropbox account id.
	// The other remote file systems have no such identifier -
	// neither SFTP, FTP, WebDAV, SMB, S3 nor Azure Blob Storage
	// expose one - so they use the coordinates that identify the
	// store instead: the bucket, the container, the share, or the
	// user and host of the connection.
	//
	// The format is therefore specific to the implementation and
	// carries no promise beyond uniqueness and stability. Use it
	// to tell two file systems apart, to key a cache, or as meta
	// information; don't parse it. Use [FileSystem.Prefix] to
	// build URIs and [FileSystem.Name] for human readable output.
	ID() string

	// Prefix returns the URI prefix for this file system (e.g., "file://", "sftp://").
	Prefix() string

	// Name returns the name of the FileSystem implementation.
	Name() string

	// String returns a descriptive string for the FileSystem implementation.
	String() string

	// Separator returns the path separator for the file system.
	Separator() string

	// ReadableWritable returns whether the file system is readable and/or writable.
	ReadableWritable() (readable, writable bool)

	// RootDir returns the file system root directory,
	// or InvalidFile for file systems without a root directory.
	RootDir() File

	// CleanPath joins the uriParts with the separator and returns
	// the cleaned file system path: the prefix of the file system
	// is stripped from the first part, "." and ".." elements and
	// duplicate separators are removed. The passed uriParts slice
	// must not be modified.
	CleanPath(uriParts ...string) string

	// Stat returns the FileInfo of the file or directory at filePath,
	// following symbolic links.
	// Returns an error wrapping os.ErrNotExist if the file does not exist.
	Stat(filePath string) (*FileInfo, error)

	// ListDir calls the passed callback function for every file and directory in dirPath.
	// If any patterns are passed, then only files or directories with a name that matches
	// at least one of the patterns are returned.
	// Canceling the context or returning an error from the callback
	// will stop the listing and return the context or callback error.
	// Does not recurse into subdirectories.
	ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error

	// OpenReader opens the file at filePath for reading.
	OpenReader(filePath string) (io.ReadCloser, error)

	// Close closes the file system and unregisters it if it was registered.
	// Calling Close more than once is valid and returns nil.
	// File systems that can't be closed do nothing.
	Close() error
}

// WriteFileSystem is implemented by file systems that can be written to.
//
// A file system that implements WriteFileSystem may still be read-only
// at runtime, which is reported by ReadableWritable.
type WriteFileSystem interface {
	FileSystem

	// OpenWriter opens the file at filePath for writing, creating it if it does not exist
	// or truncating it if it does exist.
	// A perm of zero means the default permissions of the file system.
	OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error)

	// MakeDir creates a single directory at dirPath.
	// Does not create parent directories if they don't exist.
	// Returns an error wrapping os.ErrExist if the path already exists,
	// with the exception of file systems where directories are implicit.
	// A perm of zero means the default permissions of the file system.
	MakeDir(dirPath string, perm Permissions) error

	// Remove deletes the file or empty directory at filePath.
	// Returns an error wrapping os.ErrNotExist if the path does not exist,
	// and an error for a non-empty directory.
	Remove(filePath string) error
}

// PrefixAliasFileSystem can be implemented by file systems
// that are also reachable with URI prefixes other than Prefix(),
// for example the same prefix with the default port number of the protocol.
type PrefixAliasFileSystem interface {
	FileSystem

	// PrefixAliases returns additional URI prefixes of the file system.
	PrefixAliases() []string
}

// ExistsFileSystem can be implemented by file systems
// that can check file existence more efficiently than using Stat.
type ExistsFileSystem interface {
	FileSystem

	// Exists returns if a file exists.
	// An error is returned if the existence can't be determined.
	Exists(filePath string) (bool, error)
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
	WriteFileSystem

	// WriteAll writes data to the file at filePath, creating it if it does not exist
	// or truncating it if it does exist.
	WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error
}

// AppendFileSystem can be implemented by file systems
// that can append to files more efficiently than using OpenAppendWriter.
type AppendFileSystem interface {
	WriteFileSystem

	// Append appends data to the file at filePath, creating it if it does not exist.
	Append(ctx context.Context, filePath string, data []byte, perm Permissions) error
}

// AppendWriterFileSystem can be implemented by file systems
// that have native append writer functionality.
type AppendWriterFileSystem interface {
	WriteFileSystem

	// OpenAppendWriter opens the file at filePath for appending,
	// creating it if it does not exist.
	OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error)
}

// ReadWriterFileSystem can be implemented by file systems
// that support random access reading and writing of files.
//
// If a file system does not implement this interface
// then its functionality is emulated by buffering the
// whole file in memory and writing it back on Close.
type ReadWriterFileSystem interface {
	WriteFileSystem

	// OpenReadWriter opens the file at filePath for reading and writing,
	// creating it if it does not exist but not truncating it if it does exist.
	OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error)
}

// TruncateFileSystem can be implemented by file systems
// that have native file truncation/resizing functionality.
type TruncateFileSystem interface {
	WriteFileSystem

	// Truncate resizes a file by not only
	// truncating to a smaller size but also
	// appending zeros to a bigger size
	// than the current one.
	Truncate(filePath string, size int64) error
}

// TouchFileSystem can be implemented by file systems
// that have native touch functionality.
//
// If a file system does not implement this interface
// then File.Touch creates an empty file if none exists
// and returns an ErrUnsupported error for an existing file.
type TouchFileSystem interface {
	WriteFileSystem

	// Touch creates an empty file at filePath if it does not exist,
	// or updates the modification time if it does exist.
	Touch(filePath string, perm Permissions) error
}

// MakeAllDirsFileSystem can be implemented by file systems
// that have native recursive directory creation functionality.
type MakeAllDirsFileSystem interface {
	WriteFileSystem

	// MakeAllDirs creates all directories in dirPath recursively,
	// similar to mkdir -p. No error is returned if the directory already exists.
	MakeAllDirs(dirPath string, perm Permissions) error
}

// RemoveAllFileSystem can be implemented by file systems
// that can remove a directory with all of its contents
// more efficiently than removing every entry individually.
type RemoveAllFileSystem interface {
	WriteFileSystem

	// RemoveAll removes filePath and any children it contains,
	// similar to os.RemoveAll.
	// No error is returned if the path does not exist.
	RemoveAll(ctx context.Context, filePath string) error
}

// CopyFileSystem can be implemented by file systems
// that have native file copying functionality.
type CopyFileSystem interface {
	WriteFileSystem

	// CopyFile copies a single file within the file system.
	// Copying a file onto itself is a no-op.
	CopyFile(ctx context.Context, srcFile string, destFile string) error
}

// MoveFileSystem can be implemented by file systems
// that have native file moving functionality.
type MoveFileSystem interface {
	WriteFileSystem

	// Move moves and/or renames the file or directory at filePath to destPath.
	// destPath is always the final path, never a directory to move into.
	//
	// When filePath and destPath are the same, Move must be a no-op
	// and return nil, matching the behavior of [os.Rename].
	Move(filePath string, destPath string) error
}

// RenameFileSystem can be implemented by file systems
// that have native file renaming functionality.
type RenameFileSystem interface {
	WriteFileSystem

	// Rename only renames the file in its base directory
	// but does not move it into another directory.
	Rename(filePath string, newName string) (newPath string, err error)
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

	// ListDirRecursive calls the passed callback function for every file (not directory) in dirPath
	// recursing into all sub-directories.
	// If any patterns are passed, then only files (not directories) with a name that matches
	// at least one of the patterns are returned.
	ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error
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

// HiddenFileSystem can be implemented by file systems
// with a definition of hidden files that differs from
// the default of names beginning with a dot.
type HiddenFileSystem interface {
	FileSystem

	// IsHidden returns if a file or directory is hidden.
	IsHidden(filePath string) bool
}

// PermissionsFileSystem can be implemented by file systems
// that support setting file permissions.
type PermissionsFileSystem interface {
	WriteFileSystem

	// SetPermissions sets the permissions of the file at filePath.
	SetPermissions(filePath string, perm Permissions) error
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

// SymbolicLinkFileSystem can be implemented by file systems
// that support symbolic links.
type SymbolicLinkFileSystem interface {
	FileSystem

	// IsSymbolicLink returns if the file at filePath is a symbolic link.
	IsSymbolicLink(filePath string) bool

	// CreateSymbolicLink creates a symbolic link at linkPath
	// pointing to targetPath. targetPath is stored verbatim,
	// so it may be relative to linkPath's directory or absolute,
	// depending on what the caller wants the resolved link to mean.
	CreateSymbolicLink(targetPath, linkPath string) error

	// ReadSymbolicLink returns the target of the symbolic link at linkPath
	// as stored on disk. The returned path is raw and may be relative
	// to linkPath's directory.
	ReadSymbolicLink(linkPath string) (targetPath string, err error)
}

// XAttrFileSystem extends FileSystem with support for extended file attributes (xattrs).
// Extended attributes are name-value pairs associated with files that provide
// additional metadata beyond standard file attributes.
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

// VolumeNameFileSystem should be implemented by file systems
// that have volume names.
type VolumeNameFileSystem interface {
	FileSystem

	// VolumeName returns the name of the volume at the beginning of the filePath,
	// or an empty string if the filePath has no volume.
	// A volume is for example "C:" on Windows
	VolumeName(filePath string) string
}

// AbsPathFileSystem can be implemented by file systems
// where paths can be relative to a working directory,
// like the local file system.
//
// For all other file systems every path is absolute
// and relative paths are computed by the package.
type AbsPathFileSystem interface {
	FileSystem

	// IsAbsPath indicates if the passed filePath is absolute.
	IsAbsPath(filePath string) bool

	// AbsPath returns the passed filePath in absolute form.
	// If the absolute path cannot be determined, returns the original filePath.
	AbsPath(filePath string) string

	// RelPath returns the relative path of targPath relative to basePath
	// like [filepath.Rel] but for the file system implementation.
	//
	// The returned path will always be relative to basePath, even if basePath and
	// targPath share no elements: joining basePath with the result yields a path
	// equivalent to targPath after cleaning. The returned path is cleaned
	// and may contain ".." segments. If basePath and targPath are identical,
	// "." is returned.
	//
	// An error is returned if targPath can't be made relative to basePath
	// (for example when one is absolute and the other is not),
	// or if knowing the current working directory would be necessary to compute it.
	RelPath(basePath, targPath string) (string, error)
}
