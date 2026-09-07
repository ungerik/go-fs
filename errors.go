package fs

import (
	"errors"
	"fmt"
	"net/http"
	"os"
)

// SentinelError is used for const sentinel errors
type SentinelError string

// Error implements the error interface
func (e SentinelError) Error() string {
	return string(e)
}

const (
	// ErrReadOnlyFileSystem is returned when a file system doesn't support writes
	ErrReadOnlyFileSystem SentinelError = "file system is read-only"

	// ErrWriteOnlyFileSystem is returned when a file system doesn't support reads
	ErrWriteOnlyFileSystem SentinelError = "file system is write-only"

	// ErrInvalidFileSystem indicates an invalid file system
	ErrInvalidFileSystem SentinelError = "invalid file system"

	// ErrFileSystemClosed is returned after a file system Close method was called
	ErrFileSystemClosed SentinelError = "file system is closed"

	// ErrUnmarshalJSON wraps the error of File.ReadJSON and MemFile.ReadJSON
	ErrUnmarshalJSON SentinelError = "can't unmarshal JSON"

	// ErrMarshalJSON wraps the error of File.WriteJSON and MemFile.WriteJSON
	ErrMarshalJSON SentinelError = "can't marshal JSON"

	// ErrUnmarshalXML wraps the error of File.ReadXML and MemFile.ReadXML
	ErrUnmarshalXML SentinelError = "can't unmarshal XML"

	// ErrMarshalXML wraps the error of File.WriteXML and MemFile.WriteXML
	ErrMarshalXML SentinelError = "can't marshal XML"
)

///////////////////////////////////////////////////////////////////////////////
// ErrDoesNotExist

// RemoveErrDoesNotExist returns nil if err wraps os.ErrNotExist,
// else err will be returned unchanged.
func RemoveErrDoesNotExist(err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ErrEmptyPath indicates an empty file path
var ErrEmptyPath = NewErrDoesNotExist(InvalidFile)

// ErrDoesNotExist is returned when a file does not exist
// and wraps os.ErrNotExist.
// Check for this error type with:
//
//	errors.Is(err, os.ErrNotExist)
//
// Implements http.Handler by responding with http.NotFound.
type ErrDoesNotExist struct {
	file any
}

// NewErrDoesNotExist returns a new ErrDoesNotExist
func NewErrDoesNotExist(file File) ErrDoesNotExist {
	return ErrDoesNotExist{file}
}

// NewErrDoesNotExistFileReader returns an ErrDoesNotExist
// error for a FileReader.
func NewErrDoesNotExistFileReader(fileReader FileReader) ErrDoesNotExist {
	return ErrDoesNotExist{fileReader}
}

// NewErrPathDoesNotExist returns an ErrDoesNotExist
// error for a file path.
func NewErrPathDoesNotExist(path string) ErrDoesNotExist {
	return ErrDoesNotExist{path}
}

// Error implements the error interface
func (err ErrDoesNotExist) Error() string {
	fileStr := fmt.Sprintf("%s", err.file)
	if fileStr == "" {
		return "empty file path"
	}
	return fmt.Sprintf("file does not exist: %s", fileStr)
}

// Unwrap returns os.ErrNotExist
func (ErrDoesNotExist) Unwrap() error {
	return os.ErrNotExist
}

// File returns the File that error concerns
// or false for ok if the error is not about a File
// but another type.
func (err ErrDoesNotExist) File() (file File, ok bool) {
	file, ok = err.file.(File)
	return file, ok
}

// FileReader returns the FileReader that error concerns
// or false for ok if the error is not about a FileReader
// but another type.
func (err ErrDoesNotExist) FileReader() (file FileReader, ok bool) {
	file, ok = err.file.(FileReader)
	return file, ok
}

// ServeHTTP implements http.Handler by responding with http.NotFound
func (err ErrDoesNotExist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

///////////////////////////////////////////////////////////////////////////////
// ErrPermission

// ErrPermission is returned when an operation lacks
// permissions on a file. It wraps os.ErrPermission.
// Check for this error type with:
//
//	errors.Is(err, os.ErrPermission)
//
// Implements http.Handler by responding with 403 Forbidden.
type ErrPermission struct {
	file File
}

// NewErrPermission returns a new ErrPermission
func NewErrPermission(file File) ErrPermission {
	return ErrPermission{file}
}

// Error implements the error interface
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

// ServeHTTP implements http.Handler by responding with 403 Forbidden
func (err ErrPermission) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
}

///////////////////////////////////////////////////////////////////////////////
// ErrAlreadyExists

// ErrAlreadyExists is returned when a file already exists.
// It wraps os.ErrExist, check for this error type with:
//
//	errors.Is(err, os.ErrExist)
type ErrAlreadyExists struct {
	file File
}

// NewErrAlreadyExists returns a new ErrAlreadyExists
func NewErrAlreadyExists(file File) ErrAlreadyExists {
	return ErrAlreadyExists{file}
}

// Error implements the error interface
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
	file any
}

// NewErrIsDirectory returns a new ErrIsDirectory
func NewErrIsDirectory(file any) ErrIsDirectory {
	return ErrIsDirectory{file}
}

// Error implements the error interface
func (err ErrIsDirectory) Error() string {
	fileStr := fmt.Sprintf("%s", err.file)
	if fileStr == "" {
		return "empty file path"
	}
	return fmt.Sprintf("file is a directory: %s", fileStr)
}

// File returns the File that error concerns
// or false for ok if the error is not about a File
// but another type.
func (err ErrIsDirectory) File() (file File, ok bool) {
	file, ok = err.file.(File)
	return file, ok
}

// FileReader returns the FileReader that error concerns
// or false for ok if the error is not about a FileReader
// but another type.
func (err ErrIsDirectory) FileReader() (file FileReader, ok bool) {
	file, ok = err.file.(FileReader)
	return file, ok
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

// ErrIsNotDirectory is returned when an operation is not possible
// because a file is not a directory.
type ErrIsNotDirectory struct {
	file any
}

// NewErrIsNotDirectory returns a new ErrIsNotDirectory
func NewErrIsNotDirectory(file any) ErrIsNotDirectory {
	return ErrIsNotDirectory{file}
}

// Error implements the error interface
func (err ErrIsNotDirectory) Error() string {
	fileStr := fmt.Sprintf("%s", err.file)
	if fileStr == "" {
		return "empty file path"
	}
	return fmt.Sprintf("file is not a directory: %s", fileStr)
}

// File returns the File that error concerns
// or false for ok if the error is not about a File
// but another type.
func (err ErrIsNotDirectory) File() (file File, ok bool) {
	file, ok = err.file.(File)
	return file, ok
}

// FileReader returns the FileReader that error concerns
// or false for ok if the error is not about a FileReader
// but another type.
func (err ErrIsNotDirectory) FileReader() (file FileReader, ok bool) {
	file, ok = err.file.(FileReader)
	return file, ok
}

///////////////////////////////////////////////////////////////////////////////
// translateErrFile

// translateErrFile returns err with the File of typed errors
// translated by mapFile, or err unchanged if it is not a typed
// error with a File.
func translateErrFile(err error, mapFile func(File) File) error {
	var (
		notExist ErrDoesNotExist
		exists   ErrAlreadyExists
		isDir    ErrIsDirectory
		isNotDir ErrIsNotDirectory
	)
	switch {
	case errors.As(err, &notExist):
		if f, ok := notExist.file.(File); ok {
			return NewErrDoesNotExist(mapFile(f))
		}
	case errors.As(err, &exists):
		return NewErrAlreadyExists(mapFile(exists.file))
	case errors.As(err, &isDir):
		if f, ok := isDir.file.(File); ok {
			return NewErrIsDirectory(mapFile(f))
		}
	case errors.As(err, &isNotDir):
		if f, ok := isNotDir.file.(File); ok {
			return NewErrIsNotDirectory(mapFile(f))
		}
	}
	return err
}

///////////////////////////////////////////////////////////////////////////////
// ErrUnsupported

// ErrUnsupported is returned when a file system does not support
// an operation. It wraps errors.ErrUnsupported, check for it with:
//
//	errors.Is(err, errors.ErrUnsupported)
type ErrUnsupported struct {
	fs FileSystem
	op string
}

// NewErrUnsupported returns a new ErrUnsupported
func NewErrUnsupported(fileSystem FileSystem, operation string) ErrUnsupported {
	return ErrUnsupported{fileSystem, operation}
}

// Error implements the error interface
func (err ErrUnsupported) Error() string {
	return fmt.Sprintf("%s %s at %s", errors.ErrUnsupported, err.op, err.fs)
}

// Unwrap returns errors.ErrUnsupported
func (ErrUnsupported) Unwrap() error {
	return errors.ErrUnsupported
}
