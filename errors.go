package fs

import "errors"

func filePathOrURL(file File) string {
	if file.FileSystem().IsLocal() {
		return file.Path()
	}
	return file.URL()
}

///////////////////////////////////////////////////////////////////////////////
// ErrFileWatchNotAvailable

var ErrFileWatchNotAvailable = errors.New("File.Watch() not available")

///////////////////////////////////////////////////////////////////////////////
// ErrFileDoesNotExist

type ErrFileDoesNotExist struct {
	File File
}

func (err ErrFileDoesNotExist) Error() string {
	return "file does not exist: " + filePathOrURL(err.File)
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

type ErrIsDirectory struct {
	File File
}

func (err ErrIsDirectory) Error() string {
	return "file is a directory: " + filePathOrURL(err.File)
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

type ErrIsNotDirectory struct {
	File File
}

func (err ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + filePathOrURL(err.File)
}
