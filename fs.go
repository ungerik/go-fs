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
// A local destination that is the directory entry of source under another
// spelling of its name, like a name that only differs in case on
// a case-insensitive file system, is no directory to move into,
// source is renamed to it like by [LocalFileSystem.Move].
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
// Between different file systems a symbolic link to a file is followed:
// the destination gets a copy of the file, but only the link is removed
// from the source. A symbolic link to a directory, the source itself or one
// below a moved directory, is never copied to another file system, which can
// resolve its target to another directory, see [CopyRecursive]: Move fails
// before anything is copied with an error wrapping [errors.ErrUnsupported]
// that names the link.
//
// When source and destination resolve to the same location on the same
// FileSystem, Move returns nil without touching the file. This matches
// the [os.Rename] no-op behavior required by the [MoveFileSystem]
// contract and protects against the copy-then-delete fallback path
// destroying the file when caller passes identical source and
// destination. The copy-then-delete fallback also returns nil for two
// paths of the same local file, like through bind mounts, a symbolic
// link to a parent directory, a [SubFileSystem] or the local layer of an
// [OverlayFileSystem], and renames the file for a name in the same directory
// that only differs in case, like [LocalFileSystem.Move] does it. Moving
// a local directory into itself through such a path, a local file onto
// another hard link of it, and a case-only rename through an
// OverlayFileSystem, which supports only one case of a name, are errors
// wrapping [os.ErrInvalid] that the fallback returns without changing
// anything. Moving a symbolic link onto the file it points to, which would
// replace the file with a link to itself, is an error wrapping
// [os.ErrInvalid] that tells they are the same file, like mv reports it,
// for [LocalFileSystem.Move], [MemFileSystem.Move], the native Move of
// sftpfs and smbfs and the copy-then-delete fallback, also through the local layers of an OverlayFileSystem, which
// can resolve the link to another path than the local file system, except
// for a local file with other hard links that keep it.
//
// The copy-then-delete fallback replaces a symbolic link at destination,
// never writes through it and never writes through a link below a moved
// directory, see [CopyRecursive]. It checks the destination by path before
// it writes, so these guarantees about links at the destination assume that
// destination is not changed concurrently by others.
func Move(ctx context.Context, source, destination File) error {
	if source == "" || destination == "" {
		return ErrEmptyPath
	}
	srcFS, srcPath := source.ParseRawURI()
	destFS, destPath := destination.ParseRawURI()
	if srcFS == destFS && srcPath == destPath {
		return nil
	}
	// Moving into an existing directory, unless it is the source itself
	// under another spelling of its name, which renames the source
	if destination.IsDir() && !isLocalRenameOfDirEntry(srcFS, srcPath, destFS, destPath) {
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
