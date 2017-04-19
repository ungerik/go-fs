package fs

import (
	"os"
	"strings"
)

var (
	// Local is the local file system
	Local = &LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite + AllExecute,
	}

	// Registry contains all registerred file systems
	Registry = []FileSystem{Local}
)

func DeregisterFileSystem(fs FileSystem) bool {
	prefix := fs.Prefix()
	for i, regfs := range Registry {
		if regfs.Prefix() == prefix {
			if i < len(Registry)-1 {
				Registry = append(Registry[:i], Registry[i+1:]...)
			} else {
				Registry = Registry[:i]
			}
			return true
		}
	}
	return false
}

// GetFileSystem returns a FileSystem for the passed URI.
// Returns the local file system if no different file system could be identified.
// The URI can be passed as parts that will be joined according to the file system.
func GetFileSystem(uriParts ...string) FileSystem {
	if len(uriParts) == 0 {
		return Local
	}
	return getFileSystem(uriParts[0])
}

func getFileSystem(uri string) FileSystem {
	if uri == "" {
		return Local
	}
	for _, fs := range Registry {
		if strings.HasPrefix(uri, fs.Prefix()) {
			return fs
		}
	}
	return Local
}

func CleanPath(uriParts ...string) File {
	return GetFileSystem(uriParts...).File(uriParts...)
}

func Exists(uriParts ...string) bool {
	return CleanPath(uriParts...).Exists()
}

func IsDir(uriParts ...string) bool {
	return CleanPath(uriParts...).IsDir()
}

func ListDir(uri string, callback func(File) error, patterns ...string) error {
	return CleanPath(uri).ListDir(callback, patterns...)
}

// ListDirMax: n == -1 lists all
func ListDirMax(uri string, n int, patterns ...string) ([]File, error) {
	return CleanPath(uri).ListDirMax(n, patterns...)
}

func Touch(uri string, perm ...Permissions) (File, error) {
	file := CleanPath(uri)
	err := file.Touch(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func MakeDir(uri string, perm ...Permissions) (File, error) {
	file := CleanPath(uri)
	err := file.MakeDir(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func MakeAllDirs(uri string, perm ...Permissions) (File, error) {
	file := CleanPath(uri)
	err := file.MakeAllDirs(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func Truncate(uri string, size int64) error {
	return CleanPath(uri).Truncate(size)
}

func Remove(uri string) error {
	return CleanPath(uri).Remove()
}

func ReadFile(uri string) ([]byte, error) {
	return CleanPath(uri).ReadAll()
}

func WriteFile(uri string, data []byte, perm ...Permissions) error {
	return CleanPath(uri).WriteAll(data, perm...)
}

func Append(uri string, data []byte, perm ...Permissions) error {
	return CleanPath(uri).Append(data, perm...)
}

func TempDir() File {
	return File(os.TempDir())
}
