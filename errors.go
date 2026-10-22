package fs

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
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

	// ErrEmptyPath is returned when an operation is called with an empty file path.
	// It does not wrap os.ErrNotExist, an empty path is a programming error.
	ErrEmptyPath SentinelError = "empty file path"

	// ErrFileSystemsDoNotMatch is returned by operations like Rename,
	// RelPath or CreateSymbolicLink that need both files
	// to be on the same file system, but they are not.
	ErrFileSystemsDoNotMatch SentinelError = "file systems do not match"

	// ErrAlreadyRegistered is returned by Register when another
	// file system is registered with the same prefix.
	ErrAlreadyRegistered SentinelError = "file system prefix already registered"
)

///////////////////////////////////////////////////////////////////////////////
// Typed file errors
//
// The typed file errors store the FileReader they were created for,
// usually a File. FileReader returns it, File returns it if it is a File.
//
// The FileReader is stored as *FileReader, a pointer to an interface,
// although an interface field could hold nil as well. The reason is
// comparability: errors.Is(err, target) compares err == target before
// it calls the Is method, because a struct with an interface field is
// a comparable type. Comparing two errors holding a non-comparable
// FileReader like MemFile (which contains a byte slice) would panic
// with "comparing uncomparable type", and no Is method can prevent that
// because it is called after the comparison. A pointer compares by address
// and never panics; the Is methods then match any error of the same type.
// The price is that two separately created errors are never ==,
// use errors.Is or errors.As to check them.

// RemoveErrDoesNotExist returns nil if err wraps os.ErrNotExist,
// else err will be returned unchanged.
func RemoveErrDoesNotExist(err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

///////////////////////////////////////////////////////////////////////////////
// ErrDoesNotExist

// ErrDoesNotExist is returned when a file does not exist
// and wraps os.ErrNotExist. Check for this error with:
//
//	errors.Is(err, os.ErrNotExist)
//
// Implements http.Handler by responding with http.NotFound.
type ErrDoesNotExist struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrDoesNotExist returns a new ErrDoesNotExist for a File
// or another FileReader like a MemFile.
func NewErrDoesNotExist(file FileReader) ErrDoesNotExist {
	return ErrDoesNotExist{&file}
}

// NewErrPathDoesNotExist returns an ErrDoesNotExist for a name
// or path that is not a File, like the name of a document part.
// The FileReader of the error is a MemFile without data with
// the path as FileName, so the path can't be mistaken for a local file.
func NewErrPathDoesNotExist(path string) ErrDoesNotExist {
	return NewErrDoesNotExist(MemFile{FileName: path})
}

// Error implements the error interface
func (err ErrDoesNotExist) Error() string {
	return "file does not exist: " + errSubject(err.FileReader())
}

// Unwrap returns os.ErrNotExist
func (ErrDoesNotExist) Unwrap() error {
	return os.ErrNotExist
}

// Is reports if target is an ErrDoesNotExist of any file,
// so errors.Is(err, ErrDoesNotExist{}) matches every ErrDoesNotExist.
func (ErrDoesNotExist) Is(target error) bool {
	_, ok := target.(ErrDoesNotExist)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrDoesNotExist) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrDoesNotExist) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
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
// Check for this error with:
//
//	errors.Is(err, os.ErrPermission)
//
// Implements http.Handler by responding with 403 Forbidden.
type ErrPermission struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrPermission returns a new ErrPermission for a File
// or another FileReader like a MemFile.
func NewErrPermission(file FileReader) ErrPermission {
	return ErrPermission{&file}
}

// Error implements the error interface
func (err ErrPermission) Error() string {
	return "file lacks permission: " + errSubject(err.FileReader())
}

// Unwrap returns os.ErrPermission
func (ErrPermission) Unwrap() error {
	return os.ErrPermission
}

// Is reports if target is an ErrPermission of any file,
// so errors.Is(err, ErrPermission{}) matches every ErrPermission.
func (ErrPermission) Is(target error) bool {
	_, ok := target.(ErrPermission)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrPermission) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrPermission) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
	return file, ok
}

// ServeHTTP implements http.Handler by responding with 403 Forbidden
func (err ErrPermission) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
}

///////////////////////////////////////////////////////////////////////////////
// ErrAlreadyExists

// ErrAlreadyExists is returned when a file already exists.
// It wraps os.ErrExist, check for this error with:
//
//	errors.Is(err, os.ErrExist)
type ErrAlreadyExists struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrAlreadyExists returns a new ErrAlreadyExists for a File
// or another FileReader like a MemFile.
func NewErrAlreadyExists(file FileReader) ErrAlreadyExists {
	return ErrAlreadyExists{&file}
}

// Error implements the error interface
func (err ErrAlreadyExists) Error() string {
	return "file already exists: " + errSubject(err.FileReader())
}

// Unwrap returns os.ErrExist
func (ErrAlreadyExists) Unwrap() error {
	return os.ErrExist
}

// Is reports if target is an ErrAlreadyExists of any file,
// so errors.Is(err, ErrAlreadyExists{}) matches every ErrAlreadyExists.
func (ErrAlreadyExists) Is(target error) bool {
	_, ok := target.(ErrAlreadyExists)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrAlreadyExists) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrAlreadyExists) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
	return file, ok
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

// ErrIsDirectory is returned when an operation is not possible because
// a file is a directory. Check for this error type with:
//
//	errors.Is(err, fs.ErrIsDirectory{})
type ErrIsDirectory struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrIsDirectory returns a new ErrIsDirectory for a File
// or another FileReader like a MemFile.
func NewErrIsDirectory(file FileReader) ErrIsDirectory {
	return ErrIsDirectory{&file}
}

// Error implements the error interface
func (err ErrIsDirectory) Error() string {
	return "file is a directory: " + errSubject(err.FileReader())
}

// Is reports if target is an ErrIsDirectory of any file,
// so errors.Is(err, ErrIsDirectory{}) matches every ErrIsDirectory.
func (ErrIsDirectory) Is(target error) bool {
	_, ok := target.(ErrIsDirectory)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrIsDirectory) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrIsDirectory) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
	return file, ok
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

// ErrIsNotDirectory is returned when an operation is not possible
// because a file is not a directory. Check for this error type with:
//
//	errors.Is(err, fs.ErrIsNotDirectory{})
type ErrIsNotDirectory struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrIsNotDirectory returns a new ErrIsNotDirectory for a File
// or another FileReader like a MemFile.
func NewErrIsNotDirectory(file FileReader) ErrIsNotDirectory {
	return ErrIsNotDirectory{&file}
}

// Error implements the error interface
func (err ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + errSubject(err.FileReader())
}

// Is reports if target is an ErrIsNotDirectory of any file,
// so errors.Is(err, ErrIsNotDirectory{}) matches every ErrIsNotDirectory.
func (ErrIsNotDirectory) Is(target error) bool {
	_, ok := target.(ErrIsNotDirectory)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrIsNotDirectory) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrIsNotDirectory) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
	return file, ok
}

///////////////////////////////////////////////////////////////////////////////
// ErrDirectoryNotEmpty

// ErrDirectoryNotEmpty is returned when a directory can't be removed
// because it is not empty. Check for this error type with:
//
//	errors.Is(err, fs.ErrDirectoryNotEmpty{})
type ErrDirectoryNotEmpty struct {
	file *FileReader // pointer to stay comparable, see "Typed file errors"
}

// NewErrDirectoryNotEmpty returns a new ErrDirectoryNotEmpty for a File
// or another FileReader like a MemFile.
func NewErrDirectoryNotEmpty(file FileReader) ErrDirectoryNotEmpty {
	return ErrDirectoryNotEmpty{&file}
}

// Error implements the error interface
func (err ErrDirectoryNotEmpty) Error() string {
	return "directory is not empty: " + errSubject(err.FileReader())
}

// Is reports if target is an ErrDirectoryNotEmpty of any file,
// so errors.Is(err, ErrDirectoryNotEmpty{}) matches every ErrDirectoryNotEmpty.
func (ErrDirectoryNotEmpty) Is(target error) bool {
	_, ok := target.(ErrDirectoryNotEmpty)
	return ok
}

// FileReader returns the File or other FileReader
// the error was created for, or nil for the zero value.
func (err ErrDirectoryNotEmpty) FileReader() FileReader {
	if err.file == nil {
		return nil
	}
	return *err.file
}

// File returns the File the error was created for and true,
// or false if it was created for a FileReader that is not a File.
func (err ErrDirectoryNotEmpty) File() (file File, ok bool) {
	file, ok = err.FileReader().(File)
	return file, ok
}

// errSubject returns what an error message says about fileReader:
// the URI of a File with a redacted password, the FileName of a MemFile,
// or the name of another FileReader. A name is never converted
// to a File, because it would be interpreted as a local file path.
func errSubject(fileReader FileReader) string {
	switch r := fileReader.(type) {
	case nil:
		return ""
	case File:
		// The password is redacted before the query is cut off,
		// because a password with an unencoded '?' or '#' reaches into it
		uri := fsimpl.RedactURI(r.String())
		if _, ok := r.FileSystem().(URIFileSystem); ok {
			// A query can carry secrets like presigned URL signatures
			if i := strings.IndexAny(uri, "?#"); i >= 0 {
				uri = uri[:i]
			}
		}
		return uri
	case MemFile:
		return r.FileName
	default:
		return r.Name()
	}
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
		notEmpty ErrDirectoryNotEmpty
		perm     ErrPermission
	)
	switch {
	case errors.As(err, &notExist):
		if f, ok := notExist.File(); ok {
			return NewErrDoesNotExist(mapFile(f))
		}
	case errors.As(err, &exists):
		if f, ok := exists.File(); ok {
			return NewErrAlreadyExists(mapFile(f))
		}
	case errors.As(err, &isDir):
		if f, ok := isDir.File(); ok {
			return NewErrIsDirectory(mapFile(f))
		}
	case errors.As(err, &isNotDir):
		if f, ok := isNotDir.File(); ok {
			return NewErrIsNotDirectory(mapFile(f))
		}
	case errors.As(err, &notEmpty):
		if f, ok := notEmpty.File(); ok {
			return NewErrDirectoryNotEmpty(mapFile(f))
		}
	case errors.As(err, &perm):
		if f, ok := perm.File(); ok {
			return NewErrPermission(mapFile(f))
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
	fs string // String of the FileSystem, a comparable value
	op string
}

// NewErrUnsupported returns a new ErrUnsupported
func NewErrUnsupported(fileSystem FileSystem, operation string) ErrUnsupported {
	return ErrUnsupported{fileSystem.String(), operation}
}

// Error implements the error interface
func (err ErrUnsupported) Error() string {
	return fmt.Sprintf("%s %s at %s", errors.ErrUnsupported, err.op, err.fs)
}

// Unwrap returns errors.ErrUnsupported
func (ErrUnsupported) Unwrap() error {
	return errors.ErrUnsupported
}

// Is reports if target is an ErrUnsupported of any file system and operation,
// so errors.Is(err, ErrUnsupported{}) matches every ErrUnsupported.
func (ErrUnsupported) Is(target error) bool {
	_, ok := target.(ErrUnsupported)
	return ok
}
