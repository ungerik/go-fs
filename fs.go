package fs

import (
	"context"
	"fmt"
)

// Filef is a shortcut for File(fmt.Sprintf(format, args...))
func Filef(format string, args ...any) File {
	return File(fmt.Sprintf(format, args...))
}

// CleanFilePath returns a File from uri with cleaned path and a file system prefix
func CleanFilePath(uri string) File {
	return fsJoinCleanFile(GetFileSystem(uri), uri)
}

// JoinCleanFilePath returns a File from joined and cleaned uriParts with a file system prefix
func JoinCleanFilePath(uriParts ...string) File {
	return fsJoinCleanFile(GetFileSystem(uriParts...), uriParts...)
}

// Move moves and/or renames source to destination.
// source and destination can be files or directories,
// a directory is moved with all its contents.
//
// If destination is an existing directory, then source is moved into it
// keeping its name, else destination is the new path of source.
// An existing file at that path is replaced ([ErrIsNotDirectory]
// if source is a directory), an existing directory is an error
// wrapping [os.ErrExist] and is neither merged nor deleted.
// Moving a directory into itself or one of its sub-directories
// is an error wrapping [os.ErrInvalid].
//
// If source and destination are using the same FileSystem,
// then [MoveFileSystem.Move] is used if implemented.
// Else source is copied to the destination and removed
// after the copy completely succeeded, so a failed or canceled copy,
// for example a failed upload, never deletes the source.
// Between different file systems symbolic links are followed:
// the destination gets a copy of what they point to,
// but only the links are removed from the source.
//
// When source and destination resolve to the same location on the same
// FileSystem, Move returns nil without touching the file. This matches
// the [os.Rename] no-op behavior required by the [MoveFileSystem]
// contract and protects against the copy-then-delete fallback path
// destroying the file when caller passes identical source and
// destination.
func Move(ctx context.Context, source, destination File) error {
	if source == "" || destination == "" {
		return ErrEmptyPath
	}
	srcFS, srcPath := source.ParseRawURI()
	destFS, destPath := destination.ParseRawURI()
	if srcFS == destFS && srcPath == destPath {
		return nil
	}
	// Moving into an existing directory
	if destination.IsDir() {
		destination = destination.Join(source.Name())
		destFS, destPath = destination.ParseRawURI()
	}
	if srcFS == destFS {
		return fsMove(ctx, srcFS, srcPath, destPath)
	}
	return moveByCopy(ctx, srcFS, srcPath, destFS, destPath)
}

// RemoveFiles removes all files.
// If a file does not exist, then it is skipped and not reported as error.
//
// To remove a single file, use [File.Remove].
// As method expression it can be passed as callback
// to the listing methods to remove the listed files:
//
//	err := dir.ListDir(ctx, fs.File.Remove, "*.tmp")
func RemoveFiles(files ...File) error {
	for _, file := range files {
		err := file.Remove()
		if RemoveErrDoesNotExist(err) != nil {
			return err
		}
	}
	return nil
}
