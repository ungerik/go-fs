package fs

import (
	"path"
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

func GetFileSystem(uriParts ...string) FileSystem {
	return getFileSystem(path.Join(uriParts...))
}

func getFileSystem(uri string) FileSystem {
	for _, fs := range Registry {
		if strings.HasPrefix(uri, fs.Prefix()) {
			return fs
		}
	}
	return Local
}

func GetFile(uriParts ...string) File {
	uri := path.Join(uriParts...)
	return getFileSystem(uri).File(uri)
}

func Exists(uriParts ...string) bool {
	return GetFile(uriParts...).Exists()
}

func IsDir(uriParts ...string) bool {
	return GetFile(uriParts...).IsDir()
}

func ListDir(uri string, callback func(File) error, patterns ...string) error {
	return GetFile(uri).ListDir(callback, patterns...)
}

// ListDirMax: n == -1 lists all
func ListDirMax(uri string, n int, patterns ...string) ([]File, error) {
	return GetFile(uri).ListDirMax(n, patterns...)
}

func Touch(uri string, perm ...Permissions) (File, error) {
	file := GetFile(uri)
	err := file.Touch(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func MakeDir(uri string, perm ...Permissions) (File, error) {
	file := GetFile(uri)
	err := file.MakeDir(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func Truncate(uri string, size int64) error {
	return GetFile(uri).Truncate(size)
}

func Remove(uri string) error {
	return GetFile(uri).Remove()
}

func Read(uri string) ([]byte, error) {
	return GetFile(uri).ReadAll()
}

func Write(uri string, data []byte, perm ...Permissions) error {
	return GetFile(uri).WriteAll(data, perm...)
}

func Append(uri string, data []byte, perm ...Permissions) error {
	return GetFile(uri).Append(data, perm...)
}
