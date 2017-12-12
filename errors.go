package fs

type ConstError string

func (e ConstError) Error() string {
	return string(e)
}

const (
	// ErrFileWatchNotSupported is returned when file watching is
	// not available for a file system
	ErrFileWatchNotSupported = ConstError("file system does not support watching files")

	// ErrReadOnlyFileSystem is returned when a file system doesn't support writes
	ErrReadOnlyFileSystem = ConstError("file system is read-only")
)

// FileError is an interface that is implemented by all errors
// that can reference a file.
type FileError interface {
	error

	// File returns the file that error concerns
	File() File
}

///////////////////////////////////////////////////////////////////////////////
// ErrDoesNotExist

// ErrDoesNotExist is returned when a file does not exist
type ErrDoesNotExist struct {
	file File
}

// NewErrDoesNotExist returns a new ErrDoesNotExist
func NewErrDoesNotExist(file File) *ErrDoesNotExist {
	return &ErrDoesNotExist{file}
}

func (err *ErrDoesNotExist) Error() string {
	return "file does not exist: " + err.file.String()
}

// File returns the file that error concerns
func (err *ErrDoesNotExist) File() File {
	return err.file
}

// IsErrDoesNotExist returns if err is of type *ErrDoesNotExist
func IsErrDoesNotExist(err error) bool {
	if err == nil {
		return false
	}
	_, isIt := err.(*ErrDoesNotExist)
	return isIt
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

// IsErrIsDirectory returns if err is of type *ErrIsDirectory
func IsErrIsDirectory(err error) bool {
	if err == nil {
		return false
	}
	_, isIt := err.(*ErrIsDirectory)
	return isIt
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

// IsErrIsNotDirectory returns if err is of type *ErrIsNotDirectory
func IsErrIsNotDirectory(err error) bool {
	if err == nil {
		return false
	}
	_, isIt := err.(*ErrIsNotDirectory)
	return isIt
}
