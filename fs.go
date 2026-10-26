package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Filef is a shortcut for File(fmt.Sprintf(format, args...))
func Filef(format string, args ...any) File {
	return File(fmt.Sprintf(format, args...))
}

// CleanFilePath returns a File from uri with cleaned path and a file system
// prefix. uri is resolved like by [File] and [ParseRawURI]: the
// percent-encoded characters of a URI with a scheme are decoded, a local
// path is used verbatim.
func CleanFilePath(uri string) File {
	return JoinCleanFilePath(uri)
}

// JoinCleanFilePath returns a File from joined and cleaned uriParts with
// a file system prefix, like File(uriParts[0]).Join(uriParts[1:]...)
// with a cleaned result: the first part is resolved like by [File] and
// [ParseRawURI], decoding the percent-encoded characters of a URI with
// a scheme, the other parts are names, used verbatim. A file URI without
// a path like "file://" has the empty path, which names no file, the
// other parts are not joined to it.
func JoinCleanFilePath(uriParts ...string) File {
	if len(uriParts) == 0 {
		return InvalidFile
	}
	fileSystem, path := ParseRawURI(uriParts[0])
	if isEmptyLocalPath(fileSystem, path) {
		return fsFile(fileSystem, "")
	}
	parts := uriParts[1:]
	if path != "" {
		parts = append([]string{path}, parts...)
	}
	return fsJoinCleanFile(fileSystem, parts...)
}

// Move moves and/or renames source to destination.
// source and destination can be files or directories,
// a directory is moved with all its contents.
//
// If destination is an existing directory, then source is moved into it
// keeping its name, else destination is the new path of source. A name of
// source that is not one valid name in that directory, like `..\x` on
// a file system with the separator `\`, is an error wrapping [os.ErrInvalid],
// also for a move within one file system.
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
// paths of the same file through a [SubFileSystem], also through two
// SubFileSystems of one parent, or through the layer of an [OverlayFileSystem]
// that holds the file, on every file system compared by path, and for two
// paths of the same local file, like through bind mounts or a symbolic link
// to a parent directory, and it renames a local file for a name in the same
// directory that only differs in case, like [LocalFileSystem.Move] does it.
// Moving a directory into itself through such a path, also onto itself,
// which moves it into itself because an existing directory is moved into,
// moving an entry of the base layer of an OverlayFileSystem to its path in
// the upper layer, which removing the source would remove, a local file onto
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
// destination is not changed concurrently by others. It fails with an error
// wrapping [os.ErrExist] instead of merging two source names that the
// destination doesn't tell apart, like "A.txt" and "a.txt" on a
// case-insensitive file system, and keeps the source, see [CopyRecursive].
// Before it copies anything, it fails with an error wrapping [os.ErrExist]
// for a destination file that the copy would write into instead of replacing
// it, a device, a named pipe or a socket, and with an error wrapping
// [os.ErrInvalid] for a name that is not valid for the destination file
// system, the name of destination or of an entry below a moved directory,
// also of an empty one, see [CopyRecursive]. A local file that Windows
// describes as irregular is written like a regular file, which fails with
// the error of the write over reparse points like AppExecLinks or WSL links,
// see [CopyRecursive]. A directory that another process creates at
// destination while the fallback lists the source keeps its entries:
// the fallback fails with an error wrapping [os.ErrExist] at the first
// entry of it that the copy would replace, and keeps the source.
func Move(ctx context.Context, source, destination File) error {
	srcFS, srcPath := source.ParseRawURI()
	destFS, destPath := destination.ParseRawURI()
	if srcPath == "" || destPath == "" {
		// Also a file URI without a path, like "file://" or
		// "file://localhost", which names no file, so two of them
		// are not the same file, see LocalFileSystem.CleanPath
		return ErrEmptyPath
	}
	if srcFS == destFS && srcPath == destPath {
		return nil
	}
	if destination.IsDir() {
		// Moving into an existing directory, unless it is the directory entry
		// of the source itself under another spelling of its name, which the
		// file system matches to the same entry, like another case on
		// a case-insensitive file system or another Unicode normalization on
		// APFS: that renames the source. Both must be local paths of the same
		// file per os.Lstat with other names in the same directory: that is
		// the same entry for a directory, which has no other hard links,
		// and for a name that only differs in case.
		renamesSource := false
		srcLocal, destLocal, _, bothLocal := fsLocalPaths(srcFS, srcPath, destFS, destPath)
		if bothLocal && filepath.Base(srcLocal) != filepath.Base(destLocal) {
			if srcInfo, srcErr := os.Lstat(srcLocal); srcErr == nil {
				destInfo, destErr := os.Lstat(destLocal)
				switch {
				case destErr != nil || !os.SameFile(srcInfo, destInfo):
					// Not the same file
				case srcInfo.IsDir():
					renamesSource = isLocalInSameDir(srcLocal, destLocal)
				default:
					renamesSource = isLocalSameDirEntry(srcLocal, destLocal)
				}
			}
		}
		if !renamesSource {
			// A name like `..\x` of a source with another separator,
			// or "NUL" on Windows, would be moved outside of destination
			name := source.Name()
			if err := validateNameIn(destFS, destPath, name, isHeldName(srcFS, name, destFS, destPath, name)); err != nil {
				return withNameCause(fmt.Errorf("%w: can't move %.200q into %.200q, the name %.200q is not valid there", os.ErrInvalid, errSubject(source), errSubject(destination), name), err)
			}
			destination = destination.Join(source.Name())
			destFS, destPath = destination.ParseRawURI()
		}
	}
	if srcFS == destFS {
		// moveByCopy checks destPath itself. A native move can make the
		// missing parent directories, like the one of a blob store
		_, destName := fsSplitDirAndName(destFS, destPath)
		if name, err := validateWritePathName(destFS, destPath, isHeldName(srcFS, source.Name(), destFS, destPath, destName)); err != nil {
			return withNameCause(fmt.Errorf("%w: can't move %.200q to %.200q, the name %.200q is not valid there", os.ErrInvalid, errSubject(source), errSubject(destination), name), err)
		}
		if dir, _ := fsSplitDirAndName(destFS, destPath); dir != "" {
			if err := validateNewDirs(destFS, dir); err != nil {
				return fmt.Errorf("can't move %.200q to %.200q: %w", errSubject(source), errSubject(destination), err)
			}
		}
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
