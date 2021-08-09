package fs

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
)

// SkipDir is used as a return value from File.ListDirEntriesRecursive callbacks to indicate that
// the directory named in the call is to be skipped. It is not returned
// as an error by any function.
var SkipDir = fs.SkipDir

// SentinelError is used for const sentinel errors
type SentinelError string

func (e SentinelError) Error() string {
	return string(e)
}

const (
	// ErrNotSupported is returned when a feature is not supported by a FileSystem implementation
	ErrNotSupported SentinelError = "not supported"

	// ErrReadOnlyFileSystem is returned when a file system doesn't support writes
	ErrReadOnlyFileSystem SentinelError = "file system is read-only"

	// ErrWriteOnlyFileSystem is returned when a file system doesn't support reads
	ErrWriteOnlyFileSystem SentinelError = "file system is write-only"

	// ErrInvalidFileSystem indicates an invalid file system
	ErrInvalidFileSystem SentinelError = "invalid file system"
)

///////////////////////////////////////////////////////////////////////////////
// ErrDoesNotExist

// ReplaceErrDoesNotExist returns the passed replacement error
// if err wraps os.ErrNotExist which is the case for ErrDoesNotExist.
// Without replacement err will be returned unchanged.
func ReplaceErrDoesNotExist(err, replacement error) error {
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return replacement
	}
	return err
}

// ErrEmptyPath indications an empty file path
var ErrEmptyPath = NewErrDoesNotExist("")

// ErrDoesNotExist is returned when a file does not exist
// and wraps os.ErrNotExist.
// Check for this error type with:
//   errors.Is(err, os.ErrNotExist)
// Implements http.Handler by responding with http.NotFound.
type ErrDoesNotExist struct {
	file fmt.Stringer
}

// NewErrDoesNotExist returns a new ErrDoesNotExist
func NewErrDoesNotExist(file File) ErrDoesNotExist {
	return ErrDoesNotExist{file}
}

// NewErrDoesNotExistFileReader is a hack that tries to cast fileReader to a File
// or use a pseudo File with the name fromm the FileReader if not possible.
func NewErrDoesNotExistFileReader(fileReader FileReader) ErrDoesNotExist {
	return ErrDoesNotExist{fileReader}
}

func (err ErrDoesNotExist) Error() string {
	path := err.file.String()
	if path == "" {
		return "empty file path"
	}
	return fmt.Sprintf("file does not exist: %s", path)
}

// Unwrap returns os.ErrNotExist
func (ErrDoesNotExist) Unwrap() error {
	return os.ErrNotExist
}

// File returns the file that error concerns
func (err ErrDoesNotExist) File() (file File, ok bool) {
	file, ok = err.file.(File)
	return file, ok
}

func (err ErrDoesNotExist) FileReader() (file FileReader, ok bool) {
	file, ok = err.file.(FileReader)
	return file, ok
}

func (err ErrDoesNotExist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

///////////////////////////////////////////////////////////////////////////////
// ErrPermission

// ErrPermission is returned when an operation lacks
// permissions on a file. It wraps os.ErrPermission.
// Check for this error type with:
//   errors.Is(err, os.ErrPermission)
// Implements http.Handler by responding with 403 Forbidden.
type ErrPermission struct {
	file File
}

// NewErrPermission returns a new ErrPermission
func NewErrPermission(file File) ErrPermission {
	return ErrPermission{file}
}

func (err ErrPermission) Error() string {
	return fmt.Sprintf("file lacks permission: %s", err.file)
}

// Unwrap returns os.ErrPermission
func (ErrPermission) Unwrap() error {
	return os.ErrPermission
}

// File returns the file that error concerns
func (err ErrPermission) File() File {
	return err.file
}

func (err ErrPermission) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
}

///////////////////////////////////////////////////////////////////////////////
// ErrAlreadyExists

// ErrAlreadyExists is returned when a file already exists.
// It wraps os.ErrExist, check for this error type with:
//   errors.Is(err, os.ErrExist)
type ErrAlreadyExists struct {
	file File
}

// NewErrAlreadyExists returns a new ErrAlreadyExists
func NewErrAlreadyExists(file File) ErrAlreadyExists {
	return ErrAlreadyExists{file}
}

func (err ErrAlreadyExists) Error() string {
	return fmt.Sprintf("file already exists: %s", err.file)
}

// Unwrap returns os.ErrExist
func (ErrAlreadyExists) Unwrap() error {
	return os.ErrExist
}

// File returns the file that already exists
func (err ErrAlreadyExists) File() File {
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
func NewErrIsDirectory(file File) ErrIsDirectory {
	return ErrIsDirectory{file}
}

func (err ErrIsDirectory) Error() string {
	return fmt.Sprintf("file is a directory: %s", err.file)
}

// File returns the file that error concerns
func (err ErrIsDirectory) File() File {
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
func NewErrIsNotDirectory(file File) ErrIsNotDirectory {
	return ErrIsNotDirectory{file}
}

func (err ErrIsNotDirectory) Error() string {
	return fmt.Sprintf("file is not a directory: %s", err.file)
}

// File returns the file that error concerns
func (err ErrIsNotDirectory) File() File {
	return err.file
}
