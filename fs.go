package fs

import "os"

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
