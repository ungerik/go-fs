package fs

import "errors"

///////////////////////////////////////////////////////////////////////////////
// ErrFileWatchNotAvailable

var ErrFileWatchNotAvailable = errors.New("File.Watch() not available")

///////////////////////////////////////////////////////////////////////////////
// ErrReadOnlyFileSystem

var ErrReadOnlyFileSystem = errors.New("file system is read-only")

///////////////////////////////////////////////////////////////////////////////
// ErrFileDoesNotExist

type ErrFileDoesNotExist struct {
	File File
}

func (err ErrFileDoesNotExist) Error() string {
	return "file does not exist: " + err.File.String()
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

type ErrIsDirectory struct {
	File File
}

func (err ErrIsDirectory) Error() string {
	return "file is a directory: " + err.File.String()
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

type ErrIsNotDirectory struct {
	File File
}

func (err ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + err.File.String()
}
