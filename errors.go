package fs

import (
	"errors"
	"fmt"
	"net/http"
	"os"
)

// SentryError is used for const sentry errors
type SentryError string

func (e SentryError) Error() string {
	return string(e)
}

const (
	// ErrFileWatchNotSupported is returned when file watching is
	// not available for a file system
	ErrFileWatchNotSupported = SentryError("file system does not support watching files")

	// ErrReadOnlyFileSystem is returned when a file system doesn't support writes
	ErrReadOnlyFileSystem = SentryError("file system is read-only")

	// ErrWriteOnlyFileSystem is returned when a file system doesn't support reads
	ErrWriteOnlyFileSystem = SentryError("file system is write-only")

	// ErrInvalidFileSystem indicates an invalid file system
	ErrInvalidFileSystem = SentryError("invalid file system")
)

///////////////////////////////////////////////////////////////////////////////
// ErrDoesNotExist

// RemoveErrDoesNotExist returns nil if err is or wraps ErrDoesNotExist,
// else err will be returned unchanged.
func RemoveErrDoesNotExist(err error) error {
	if err != nil && errors.Is(err, new(ErrDoesNotExist)) {
		return nil
	}
	return err
}

// ErrDoesNotExist is returned when a file does not exist
// Implements http.Handler with http.NotFound
// and wraps os.ErrNotExist (returned by Unwrap).
type ErrDoesNotExist struct {
	file interface{}
}

// NewErrDoesNotExist returns a new ErrDoesNotExist
func NewErrDoesNotExist(file File) *ErrDoesNotExist {
	return &ErrDoesNotExist{file}
}

// NewErrDoesNotExistFileReader is a hack that tries to cast fileReader to a File
// or use a pseudo File with the name fromm the FileReader if not possible.
func NewErrDoesNotExistFileReader(fileReader FileReader) *ErrDoesNotExist {
	return &ErrDoesNotExist{fileReader}
}

func (err *ErrDoesNotExist) Error() string {
	return fmt.Sprintf("file does not exist: %s", err.file)
}

func (*ErrDoesNotExist) Is(target error) bool {
	_, is := target.(*ErrDoesNotExist)
	return is
}

// Unwrap returns os.ErrNotExist
func (err *ErrDoesNotExist) Unwrap() error {
	return os.ErrNotExist
}

// File returns the file that error concerns
func (err *ErrDoesNotExist) File() (file File, ok bool) {
	file, ok = err.file.(File)
	return file, ok
}

func (err *ErrDoesNotExist) FileReader() (file FileReader, ok bool) {
	file, ok = err.file.(FileReader)
	return file, ok
}

func (err *ErrDoesNotExist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
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
	return fmt.Sprintf("file is a directory: %s", err.file)
}

func (*ErrIsDirectory) Is(target error) bool {
	_, is := target.(*ErrIsDirectory)
	return is
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
	return fmt.Sprintf("file is not a directory: %s", err.file)
}

func (*ErrIsNotDirectory) Is(target error) bool {
	_, is := target.(*ErrIsNotDirectory)
	return is
}

// File returns the file that error concerns
func (err *ErrIsNotDirectory) File() File {
	return err.file
}
