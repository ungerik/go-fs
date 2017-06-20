package fs

import "os"

func FileFrom(uriParts ...string) File {
	return GetFileSystem(uriParts...).File(uriParts...)
}

func Exists(uriParts ...string) bool {
	return FileFrom(uriParts...).Exists()
}

func IsDir(uriParts ...string) bool {
	return FileFrom(uriParts...).IsDir()
}

func ListDir(uri string, callback func(File) error, patterns ...string) error {
	return FileFrom(uri).ListDir(callback, patterns...)
}

// ListDirMax: n == -1 lists all
func ListDirMax(uri string, n int, patterns ...string) ([]File, error) {
	return FileFrom(uri).ListDirMax(n, patterns...)
}

func Touch(uri string, perm ...Permissions) (File, error) {
	file := FileFrom(uri)
	err := file.Touch(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func MakeDir(uri string, perm ...Permissions) (File, error) {
	file := FileFrom(uri)
	err := file.MakeDir(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func MakeAllDirs(uri string, perm ...Permissions) (File, error) {
	file := FileFrom(uri)
	err := file.MakeAllDirs(perm...)
	if err != nil {
		return "", err
	}
	return file, nil
}

func Truncate(uri string, size int64) error {
	return FileFrom(uri).Truncate(size)
}

// Move moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
func Move(source, destination File) error {
	srcFS, srcPath := source.ParseRawURI()
	destFS, destPath := destination.ParseRawURI()
	if srcFS == destFS {
		return srcFS.Move(srcPath, destPath)
	}
	err := CopyFile(source, destination)
	if err != nil {
		return err
	}
	return source.Remove()
}

// Remove removes all files with fileURIs.
// If a file does not exist, then it is skipped and not reported as error.
func Remove(fileURIs ...string) error {
	for _, uri := range fileURIs {
		err := FileFrom(uri).Remove()
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
	return FileFrom(uri).ReadAll()
}

func WriteFile(uri string, data []byte, perm ...Permissions) error {
	return FileFrom(uri).WriteAll(data, perm...)
}

func Append(uri string, data []byte, perm ...Permissions) error {
	return FileFrom(uri).Append(data, perm...)
}

func TempDir() File {
	return File(os.TempDir())
}
