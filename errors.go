package fs

import "errors"

var (
	// ErrFileWatchNotAvailable is returned when file watching is
	// not available for a file system
	ErrFileWatchNotAvailable = errors.New("file system does not support watching files")

	// ErrReadOnlyFileSystem is returned when a file system doesn't support writes
	ErrReadOnlyFileSystem = errors.New("file system is read-only")

	// ErrAbortListDir can be used as error returned by the callback function
	// of File.ListDir to abort the listing of files. It has no other side effect.
	ErrAbortListDir = errors.New("abort ListDir")
)

// FileError is an interface that is implemented by all errors
// that can reference a file.
type FileError interface {
	error

	// File returns the file that error concerns
	File() File
}

///////////////////////////////////////////////////////////////////////////////
// ErrFileDoesNotExist

// ErrFileDoesNotExist is returned when a file does not exist
type ErrFileDoesNotExist struct {
	file File
}

// NewErrFileDoesNotExist returns a new ErrFileDoesNotExist
func NewErrFileDoesNotExist(file File) *ErrFileDoesNotExist {
	return &ErrFileDoesNotExist{file}
}

func (err *ErrFileDoesNotExist) Error() string {
	return "file does not exist: " + err.file.String()
}

// File returns the file that error concerns
func (err *ErrFileDoesNotExist) File() File {
	return err.file
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

// ErrIsDirectory is returned when an operation is not possible because
// a file is a directory.
type ErrIsDirectory struct {
	file File
}

// NewErrIsDirectory returns a new ErrIsDirectory
func NewErrIsDirectory(file File) *ErrIsDirectory {
	return &ErrIsDirectory{file}
}

func (err *ErrIsDirectory) Error() string {
	return "file is a directory: " + err.file.String()
}

// File returns the file that error concerns
func (err *ErrIsDirectory) File() File {
	return err.file
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

// ErrIsNotDirectory is returned when an operation is not possible
// because a file is not a directory.
type ErrIsNotDirectory struct {
	file File
}

// NewErrIsNotDirectory returns a new ErrIsNotDirectory
func NewErrIsNotDirectory(file File) *ErrIsNotDirectory {
	return &ErrIsNotDirectory{file}
}

func (err *ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + err.file.String()
}

// File returns the file that error concerns
func (err *ErrIsNotDirectory) File() File {
	return err.file
}
