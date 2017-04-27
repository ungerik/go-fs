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

// Remove removes all files with fileURIs.
// If a file does not exist, then it is skipped and not reported as error.
func Remove(fileURIs ...string) error {
	for _, uri := range fileURIs {
		err := CleanPath(uri).Remove()
		if err != nil && !IsErrDoesNotExist(err) {
			return err
		}
	}
	return nil
}

// RemoveFiles removes all files.
// If a file does not exist, then it is skipped and not reported as error.
func RemoveFiles(files ...File) error {
	for _, file := range files {
		err := file.Remove()
		if err != nil && !IsErrDoesNotExist(err) {
			return err
		}
	}
	return nil
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
