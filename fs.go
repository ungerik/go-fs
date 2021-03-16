package fs

import "fmt"

// Filef is a shortcut for File(fmt.Sprintf(format, args...))
func Filef(format string, args ...interface{}) File {
	return File(fmt.Sprintf(format, args...))
}

// CleanFilePath returns a File from uri with cleaned path and a file system prefix
func CleanFilePath(uri string) File {
	return GetFileSystem(uri).JoinCleanFile(uri)
}

// JoinCleanFilePath returns a File from joined and cleaned uriParts with a file system prefix
func JoinCleanFilePath(uriParts ...string) File {
	return GetFileSystem(uriParts...).JoinCleanFile(uriParts...)
}

// Move moves and/or renames source to destination.
// source and destination can be files or directories.
// If source is a directory, it will be moved with all its contents.
// If source and destination are using the same FileSystem,
// then FileSystem.Move will be used, else source will be
// copied recursively first to destination and then deleted.
func Move(source, destination File) error {
	if source == "" || destination == "" {
		return ErrEmptyPath
	}

	srcFS, srcPath := source.ParseRawURI()
	destFS, destPath := destination.ParseRawURI()
	if srcFS == destFS {
		return srcFS.Move(srcPath, destPath)
	}

	err := CopyRecursive(source, destination)
	if err != nil {
		return err
	}
	return source.RemoveRecursive()
}

// Remove removes all files with fileURIs.
// If a file does not exist, then it is skipped and not reported as error.
func Remove(fileURIs ...string) error {
	for _, uri := range fileURIs {
		err := File(uri).Remove()
		if RemoveErrDoesNotExist(err) != nil {
			return err
		}
	}
	return nil
}

// RemoveFile removes a single file.
// It's just a wrapper for calling file.Remove(),
// useful mostly as callback for methods that list files
// to delete all files of a certain pattern.
// Or as a more elegant way to remove a file passed as string literal path:
//   fs.RemoveFile("/my/hardcoded.path")
func RemoveFile(file File) error {
	return file.Remove()
}

// RemoveFiles removes all files.
// If a file does not exist, then it is skipped and not reported as error.
func RemoveFiles(files ...File) error {
	for _, file := range files {
		err := file.Remove()
		if RemoveErrDoesNotExist(err) != nil {
			return err
		}
	}
	return nil
}
