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
// The typed file errors are constructed for a FileReader,
// usually a File. Their File method returns the File and true,
// or false if the error is about a FileReader that is not a File,
// like a MemFile, whose name is never converted to a File,
// because it would be interpreted as a local file path.
// FileReader returns the FileReader the error was created for.
// The errors store a pointer to a FileReader that is not a File,
// so they are always comparable and errors.Is never panics.

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
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrDoesNotExist returns a new ErrDoesNotExist for a File
// or another FileReader, see ErrDoesNotExist.File.
func NewErrDoesNotExist(file FileReader) ErrDoesNotExist {
	f, reader := fileReaderSubject(file)
	return ErrDoesNotExist{file: f, reader: reader}
}

// NewErrPathDoesNotExist returns an ErrDoesNotExist
// for a name or path that is not a File, like the name
// of a document part, so the File method of the error
// returns false and the name can't be mistaken for a local file.
func NewErrPathDoesNotExist(path string) ErrDoesNotExist {
	return ErrDoesNotExist{name: path}
}

// Error implements the error interface
func (err ErrDoesNotExist) Error() string {
	return "file does not exist: " + errSubject(err.file, err.reader, err.name)
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

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrDoesNotExist) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrDoesNotExist) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
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
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrPermission returns a new ErrPermission for a File
// or another FileReader, see ErrPermission.File.
func NewErrPermission(file FileReader) ErrPermission {
	f, reader := fileReaderSubject(file)
	return ErrPermission{file: f, reader: reader}
}

// Error implements the error interface
func (err ErrPermission) Error() string {
	return "file lacks permission: " + errSubject(err.file, err.reader, err.name)
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

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrPermission) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrPermission) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
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
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrAlreadyExists returns a new ErrAlreadyExists for a File
// or another FileReader, see ErrAlreadyExists.File.
func NewErrAlreadyExists(file FileReader) ErrAlreadyExists {
	f, reader := fileReaderSubject(file)
	return ErrAlreadyExists{file: f, reader: reader}
}

// Error implements the error interface
func (err ErrAlreadyExists) Error() string {
	return "file already exists: " + errSubject(err.file, err.reader, err.name)
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

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrAlreadyExists) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrAlreadyExists) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsDirectory

// ErrIsDirectory is returned when an operation is not possible because
// a file is a directory. Check for this error type with:
//
//	errors.Is(err, fs.ErrIsDirectory{})
type ErrIsDirectory struct {
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrIsDirectory returns a new ErrIsDirectory for a File
// or another FileReader, see ErrIsDirectory.File.
func NewErrIsDirectory(file FileReader) ErrIsDirectory {
	f, reader := fileReaderSubject(file)
	return ErrIsDirectory{file: f, reader: reader}
}

// Error implements the error interface
func (err ErrIsDirectory) Error() string {
	return "file is a directory: " + errSubject(err.file, err.reader, err.name)
}

// Is reports if target is an ErrIsDirectory of any file,
// so errors.Is(err, ErrIsDirectory{}) matches every ErrIsDirectory.
func (ErrIsDirectory) Is(target error) bool {
	_, ok := target.(ErrIsDirectory)
	return ok
}

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrIsDirectory) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrIsDirectory) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
}

///////////////////////////////////////////////////////////////////////////////
// ErrIsNotDirectory

// ErrIsNotDirectory is returned when an operation is not possible
// because a file is not a directory. Check for this error type with:
//
//	errors.Is(err, fs.ErrIsNotDirectory{})
type ErrIsNotDirectory struct {
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrIsNotDirectory returns a new ErrIsNotDirectory for a File
// or another FileReader, see ErrIsNotDirectory.File.
func NewErrIsNotDirectory(file FileReader) ErrIsNotDirectory {
	f, reader := fileReaderSubject(file)
	return ErrIsNotDirectory{file: f, reader: reader}
}

// Error implements the error interface
func (err ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + errSubject(err.file, err.reader, err.name)
}

// Is reports if target is an ErrIsNotDirectory of any file,
// so errors.Is(err, ErrIsNotDirectory{}) matches every ErrIsNotDirectory.
func (ErrIsNotDirectory) Is(target error) bool {
	_, ok := target.(ErrIsNotDirectory)
	return ok
}

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrIsNotDirectory) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrIsNotDirectory) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
}

///////////////////////////////////////////////////////////////////////////////
// ErrDirectoryNotEmpty

// ErrDirectoryNotEmpty is returned when a directory can't be removed
// because it is not empty. Check for this error type with:
//
//	errors.Is(err, fs.ErrDirectoryNotEmpty{})
type ErrDirectoryNotEmpty struct {
	file   File        // empty if the error is not about a File
	reader *FileReader // FileReader that is not a File, a pointer to stay comparable
	name   string      // name or path that is not a File, for the message
}

// NewErrDirectoryNotEmpty returns a new ErrDirectoryNotEmpty for a File
// or another FileReader, see ErrDirectoryNotEmpty.File.
func NewErrDirectoryNotEmpty(file FileReader) ErrDirectoryNotEmpty {
	f, reader := fileReaderSubject(file)
	return ErrDirectoryNotEmpty{file: f, reader: reader}
}

// Error implements the error interface
func (err ErrDirectoryNotEmpty) Error() string {
	return "directory is not empty: " + errSubject(err.file, err.reader, err.name)
}

// Is reports if target is an ErrDirectoryNotEmpty of any file,
// so errors.Is(err, ErrDirectoryNotEmpty{}) matches every ErrDirectoryNotEmpty.
func (ErrDirectoryNotEmpty) Is(target error) bool {
	_, ok := target.(ErrDirectoryNotEmpty)
	return ok
}

// File returns the File that the error concerns and true,
// or false if the error concerns a FileReader that is not a File.
func (err ErrDirectoryNotEmpty) File() (file File, ok bool) {
	return err.file, err.file != ""
}

// FileReader returns the File or other FileReader that the error concerns,
// or nil if the error does not concern a FileReader.
func (err ErrDirectoryNotEmpty) FileReader() FileReader {
	return errFileReader(err.file, err.reader)
}

// fileReaderSubject returns fileReader as File if it is one,
// else a pointer to fileReader, so that the error stays comparable
// even for FileReaders like MemFile that are not comparable.
func fileReaderSubject(fileReader FileReader) (file File, reader *FileReader) {
	if file, ok := fileReader.(File); ok {
		return file, nil
	}
	if fileReader == nil {
		return InvalidFile, nil
	}
	return InvalidFile, &fileReader
}

// errFileReader returns the FileReader of a typed error.
func errFileReader(file File, reader *FileReader) FileReader {
	switch {
	case file != "":
		return file
	case reader != nil:
		return *reader
	}
	return nil
}

// errSubject returns the file, FileReader name or name for an error message,
// the password of a URI is redacted.
func errSubject(file File, reader *FileReader, name string) string {
	switch {
	case file != "":
		return redactURI(file.String())
	case reader != nil:
		return (*reader).Name()
	}
	return name
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
	case errors.As(err, &notExist) && notExist.file != "":
		return ErrDoesNotExist{file: mapFile(notExist.file)}
	case errors.As(err, &exists) && exists.file != "":
		return ErrAlreadyExists{file: mapFile(exists.file)}
	case errors.As(err, &isDir) && isDir.file != "":
		return ErrIsDirectory{file: mapFile(isDir.file)}
	case errors.As(err, &isNotDir) && isNotDir.file != "":
		return ErrIsNotDirectory{file: mapFile(isNotDir.file)}
	case errors.As(err, &notEmpty) && notEmpty.file != "":
		return ErrDirectoryNotEmpty{file: mapFile(notEmpty.file)}
	case errors.As(err, &perm) && perm.file != "":
		return ErrPermission{file: mapFile(perm.file)}
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
