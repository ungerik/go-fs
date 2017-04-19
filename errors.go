package fs

import "errors"

///////////////////////////////////////////////////////////////////////////////
// ErrFileWatchNotAvailable

var ErrFileWatchNotAvailable = errors.New("File.Watch() not available")

///////////////////////////////////////////////////////////////////////////////
// ErrReadOnlyFileSystem

var ErrReadOnlyFileSystem = errors.New("file system is read-only")

// ErrAbortListDir can be used as error returned by the callback function
// of File.ListDir to abort the listing of files. It has no other side effect.
var ErrAbortListDir = errors.New("abort ListDir")

///////////////////////////////////////////////////////////////////////////////
// FileError

// FileError is an interface that is implemented by all errors
// that can reference a file.
type FileError interface {
	error
	File() File
}

///////////////////////////////////////////////////////////////////////////////
// ErrFileDoesNotExist

type ErrFileDoesNotExist struct {
	file File
}

func NewErrFileDoesNotExist(file File) *ErrFileDoesNotExist {
	return &ErrFileDoesNotExist{file}
}

func (err *ErrFileDoesNotExist) Error() string {
	return "file does not exist: " + err.file.String()
}

func (err *ErrFileDoesNotExist) File() File {
	return err.file
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

type ErrIsDirectory struct {
	file File
}

func NewErrIsDirectory(file File) *ErrIsDirectory {
	return &ErrIsDirectory{file}
}

func (err *ErrIsDirectory) Error() string {
	return "file is a directory: " + err.file.String()
}

func (err *ErrIsDirectory) File() File {
	return err.file
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

type ErrIsNotDirectory struct {
	file File
}

func NewErrIsNotDirectory(file File) *ErrIsNotDirectory {
	return &ErrIsNotDirectory{file}
}

func (err *ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + err.file.String()
}

func (err *ErrIsNotDirectory) File() File {
	return err.file
}
