package fs

import "os"

func JoinCleanFile(uriParts ...string) File {
	return GetFileSystem(uriParts...).JoinCleanFile(uriParts...)
}

func TempDir() File {
	return File(os.TempDir())
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
		err := JoinCleanFile(uri).Remove()
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
