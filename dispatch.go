package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

// This file contains the functions that the File methods use to call
// FileSystem implementations: the derived path helpers that every
// file system gets from its Prefix and Separator, the readable/writable
// gates that turn missing write support into ErrReadOnlyFileSystem,
// and the generic emulations of the optional interfaces.

// errStopListing is returned from listing callbacks to stop
// a listing early without reporting an error to the caller.
const errStopListing SentinelError = "stop listing"

///////////////////////////////////////////////////////////////////////////////
// Derived path helpers

// fsURI returns the URI of a clean path within fileSystem.
func fsURI(fileSystem FileSystem, cleanPath string) string {
	if local, ok := fileSystem.(*LocalFileSystem); ok {
		return local.URI(cleanPath)
	}
	if u, ok := fileSystem.(URIFileSystem); ok {
		return u.URIFromPath(cleanPath)
	}
	// Same rule as PathHelper.URI: a prefix ending with the separator
	// after a host doesn't double it, a scheme-only prefix like "x://"
	// is followed by the whole rooted path
	return fsimpl.PathHelper{URIPrefix: fileSystem.Prefix(), PathSep: fileSystem.Separator()}.URI(cleanPath)
}

// fsFile returns the File for a clean path within fileSystem.
// Files of the local file system have no prefix.
func fsFile(fileSystem FileSystem, cleanPath string) File {
	if _, ok := fileSystem.(*LocalFileSystem); ok {
		if cleanPath == "" {
			// The empty path of a file URI without a path, which names no
			// file, see fileURILocalPath, File("") would be InvalidFile
			return File(LocalPrefix)
		}
		// A path that begins with a directory named "~", like "~" that
		// TrimExt makes of "~.zip", stays below the current directory
		// instead of naming the home directory
		return localFile(cleanPath)
	}
	if _, ok := fileSystem.(InvalidFileSystem); ok && cleanPath == "" {
		return InvalidFile
	}
	return File(fsURI(fileSystem, cleanPath))
}

// fsJoinCleanFile returns the File for the cleaned and joined uriParts.
func fsJoinCleanFile(fileSystem FileSystem, uriParts ...string) File {
	return fsFile(fileSystem, fileSystem.CleanPath(uriParts...))
}

// fsVolumeLen returns the length of the volume name at the beginning of filePath.
func fsVolumeLen(fileSystem FileSystem, filePath string) int {
	if v, ok := fileSystem.(VolumeNameFileSystem); ok {
		return len(v.VolumeName(filePath))
	}
	return 0
}

// fsSplitDirAndName returns the parent directory and the name of the last element of filePath.
func fsSplitDirAndName(fileSystem FileSystem, filePath string) (dir, name string) {
	if u, ok := fileSystem.(URIFileSystem); ok {
		return u.SplitDirAndName(filePath)
	}
	return fsimpl.SplitDirAndName(filePath, fsVolumeLen(fileSystem, filePath), fileSystem.Separator())
}

// fsSplitPath returns the elements of filePath without prefix and volume.
func fsSplitPath(fileSystem FileSystem, filePath string) []string {
	filePath = strings.TrimPrefix(filePath, fileSystem.Prefix())
	return fsimpl.SplitPath(filePath[fsVolumeLen(fileSystem, filePath):], fileSystem.Separator())
}

// fsIsHidden returns if filePath is hidden using HiddenFileSystem
// if implemented, else the rule that names beginning with a dot are hidden.
func fsIsHidden(fileSystem FileSystem, filePath string) bool {
	if h, ok := fileSystem.(HiddenFileSystem); ok {
		return h.IsHidden(filePath)
	}
	_, name := fsSplitDirAndName(fileSystem, filePath)
	return strings.HasPrefix(name, ".")
}

// fsAbsPath returns filePath in absolute form.
func fsAbsPath(fileSystem FileSystem, filePath string) string {
	if a, ok := fileSystem.(AbsPathFileSystem); ok {
		return a.AbsPath(filePath)
	}
	return filePath
}

// completeFileInfo fills File, Name and IsHidden of a FileInfo
// returned by a FileSystem implementation if they are empty.
func completeFileInfo(fileSystem FileSystem, dirPath string, info *FileInfo) *FileInfo {
	if info.File == "" && info.Name != "" {
		info.File = fsJoinCleanFile(fileSystem, dirPath, info.Name)
	}
	if info.Name == "" && info.File != "" {
		info.Name = info.File.Name()
	}
	if !info.IsHidden && strings.HasPrefix(info.Name, ".") {
		info.IsHidden = fsIsHidden(fileSystem, info.File.Path())
	}
	return info
}

///////////////////////////////////////////////////////////////////////////////
// Readable / writable gates

// fsReadable returns an error if fileSystem is not readable.
func fsReadable(fileSystem FileSystem) error {
	if readable, _ := fileSystem.ReadableWritable(); !readable {
		return fmt.Errorf("%s: %w", fileSystem.Name(), ErrWriteOnlyFileSystem)
	}
	return nil
}

// fsWritable returns fileSystem as WriteFileSystem
// or an error if it is not writable.
func fsWritable(fileSystem FileSystem) (WriteFileSystem, error) {
	w, ok := fileSystem.(WriteFileSystem)
	if !ok {
		return nil, fmt.Errorf("%s: %w", fileSystem.Name(), ErrReadOnlyFileSystem)
	}
	if _, writable := fileSystem.ReadableWritable(); !writable {
		return nil, fmt.Errorf("%s: %w", fileSystem.Name(), ErrReadOnlyFileSystem)
	}
	return w, nil
}

///////////////////////////////////////////////////////////////////////////////
// Primitives with gates

func fsStat(fileSystem FileSystem, filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	info, err := fileSystem.Stat(filePath)
	if err != nil {
		return nil, err
	}
	dir, _ := fsSplitDirAndName(fileSystem, filePath)
	return completeFileInfo(fileSystem, dir, info), nil
}

func fsLstat(fileSystem FileSystem, filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	l, ok := fileSystem.(SymbolicLinkFileSystem)
	if !ok {
		return fsStat(fileSystem, filePath)
	}
	info, err := l.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	dir, _ := fsSplitDirAndName(fileSystem, filePath)
	return completeFileInfo(fileSystem, dir, info), nil
}

func fsListDir(ctx context.Context, fileSystem FileSystem, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return err
	}
	return fileSystem.ListDir(ctx, dirPath, patterns, func(info *FileInfo) error {
		return callback(completeFileInfo(fileSystem, dirPath, info))
	})
}

func fsOpenReader(fileSystem FileSystem, filePath string) (io.ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	return fileSystem.OpenReader(filePath)
}

func fsOpenWriter(fileSystem FileSystem, filePath string, perm Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return nil, err
	}
	return w.OpenWriter(filePath, perm)
}

func fsMakeDir(fileSystem FileSystem, dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	return w.MakeDir(dirPath, perm)
}

func fsRemove(fileSystem FileSystem, filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	return w.Remove(filePath)
}

///////////////////////////////////////////////////////////////////////////////
// Emulations of optional interfaces

func fsExists(fileSystem FileSystem, filePath string) (bool, error) {
	if filePath == "" {
		return false, nil
	}
	if e, ok := fileSystem.(ExistsFileSystem); ok {
		return e.Exists(filePath)
	}
	_, err := fileSystem.Stat(filePath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func fsReadAll(ctx context.Context, fileSystem FileSystem, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	if r, ok := fileSystem.(ReadAllFileSystem); ok {
		return r.ReadAll(ctx, filePath)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, err := fileSystem.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ReadAll(ctx, reader)
}

func fsWriteAll(ctx context.Context, fileSystem FileSystem, filePath string, data []byte, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if wa, ok := w.(WriteAllFileSystem); ok {
		return wa.WriteAll(ctx, filePath, data, perm)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// OpenWriter truncates, so writing fewer bytes over an
	// existing larger file does not leave stale trailing bytes.
	writer, err := w.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	return commitOrAbort(ctx, writer, WriteAll(ctx, writer, data))
}

// commitOrAbort finishes writing to writer after the write returned err.
//
// If err is not nil or ctx is canceled, the written data must not be committed:
// writer is aborted if it implements [WriteAborter], else it is closed,
// and err is returned joined with the error of Abort or Close.
// Else writer is closed, committing the data, and the error of Close
// is returned, which reports for example a failed upload.
func commitOrAbort(ctx context.Context, writer io.WriteCloser, err error) error {
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		return writer.Close()
	}
	if aborter, ok := writer.(WriteAborter); ok {
		return errors.Join(err, aborter.Abort())
	}
	return errors.Join(err, writer.Close())
}

func fsOpenAppendWriter(fileSystem FileSystem, filePath string, perm Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return nil, err
	}
	if aw, ok := w.(AppendWriterFileSystem); ok {
		return aw.OpenAppendWriter(filePath, perm)
	}
	// Emulate the append writer by reading the file into a buffer
	// and writing everything back to the file when the buffer is closed.
	current, err := fsReadAll(context.Background(), fileSystem, filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	buf := fsimpl.NewWriteOnCloseFileBuffer(current, func(data []byte) error {
		return fsWriteAll(context.Background(), fileSystem, filePath, data, perm)
	})
	// Writes must append after the current content
	_, err = buf.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func fsAppend(ctx context.Context, fileSystem FileSystem, filePath string, data []byte, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if a, ok := w.(AppendFileSystem); ok {
		return a.Append(ctx, filePath, data, perm)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if aw, ok := w.(AppendWriterFileSystem); ok {
		writer, err := aw.OpenAppendWriter(filePath, perm)
		if err != nil {
			return err
		}
		return commitOrAbort(ctx, writer, WriteAll(ctx, writer, data))
	}
	// Emulate append by reading the whole file
	// and writing it back with the appended data.
	current, err := fsReadAll(ctx, fileSystem, filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fsWriteAll(ctx, fileSystem, filePath, append(current, data...), perm)
}

func fsOpenReadWriter(fileSystem FileSystem, filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return nil, err
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	if rw, ok := w.(ReadWriterFileSystem); ok {
		return rw.OpenReadWriter(filePath, perm)
	}
	// Emulate random access by buffering the whole file in memory
	// and writing it back on Close.
	return fsimpl.NewReadWriteAllSeekCloser(
		func() ([]byte, error) {
			data, err := fsReadAll(context.Background(), fileSystem, filePath)
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil // New file
			}
			return data, err
		},
		func(data []byte) error {
			return fsWriteAll(context.Background(), fileSystem, filePath, data, perm)
		},
		nil,
	), nil
}

func fsTruncate(ctx context.Context, fileSystem FileSystem, filePath string, size int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if size < 0 {
		return fmt.Errorf("negative file size: %d", size)
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if t, ok := w.(TruncateFileSystem); ok {
		return t.Truncate(ctx, filePath, size)
	}
	// Emulate by rewriting the file
	info, err := fsStat(fileSystem, filePath)
	if err != nil {
		return err
	}
	if info.IsDir {
		return NewErrIsDirectory(info.File)
	}
	if info.Size == size {
		return nil
	}
	data, err := fsReadAll(ctx, fileSystem, filePath)
	if err != nil {
		return err
	}
	if int64(len(data)) > size {
		data = data[:size]
	} else {
		data = append(data, make([]byte, size-int64(len(data)))...)
	}
	return fsWriteAll(ctx, fileSystem, filePath, data, info.Permissions)
}

func fsTouch(fileSystem FileSystem, filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if t, ok := w.(TouchFileSystem); ok {
		return t.Touch(filePath, perm)
	}
	// Emulation: only create a missing file, never truncate an existing one
	exists, err := fsExists(fileSystem, filePath)
	if err != nil {
		return err
	}
	if exists {
		return NewErrUnsupported(fileSystem, "Touch of an existing file")
	}
	writer, err := w.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	return writer.Close()
}

func fsMakeAllDirs(fileSystem FileSystem, dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if _, ok := w.(ValidateNameFileSystem); ok {
		// A file system like a zip writer can't remove directories, so the
		// names of the missing ones are checked before the first one is made
		for p := dirPath; ; {
			if _, err := w.Stat(p); err == nil {
				break
			}
			if err := validateWriteDirName(w, p); err != nil {
				return err
			}
			dir, name := fsSplitDirAndName(w, p)
			if name == "" || dir == p {
				break
			}
			p = dir
		}
	}
	if m, ok := w.(MakeAllDirsFileSystem); ok {
		return m.MakeAllDirs(dirPath, perm)
	}
	return makeAllDirs(w, dirPath, perm)
}

// validateWriteDirName returns an error wrapping the error of the ValidateName
// method of the file system that the writes to dirPath of fileSystem go to,
// see writeTarget, if it is a ValidateNameFileSystem that rejects the name of
// the directory dirPath.
func validateWriteDirName(fileSystem FileSystem, dirPath string) error {
	target, targetPath, _, ok := writeTarget(fileSystem, dirPath)
	v, validates := target.(ValidateNameFileSystem)
	if !ok || !validates {
		return nil
	}
	if _, name := fsSplitDirAndName(target, targetPath); name != "" {
		if err := v.ValidateName(name); err != nil {
			return fmt.Errorf("can't make directory %q: %w", errSubject(fsFile(fileSystem, dirPath)), err)
		}
	}
	return nil
}

// makeAllDirs recursively creates dirPath and its parents.
func makeAllDirs(w WriteFileSystem, dirPath string, perm Permissions) error {
	if info, err := w.Stat(dirPath); err == nil {
		if !info.IsDir {
			return NewErrIsNotDirectory(fsFile(w, dirPath))
		}
		return nil
	}
	dir, name := fsSplitDirAndName(w, dirPath)
	if name != "" && dir != dirPath {
		err := makeAllDirs(w, dir, perm)
		if err != nil {
			return err
		}
	}
	err := w.MakeDir(dirPath, perm)
	if err != nil && errors.Is(err, os.ErrExist) {
		// Created concurrently
		if info, statErr := w.Stat(dirPath); statErr == nil && info.IsDir {
			return nil
		}
	}
	return err
}

func fsRemoveAll(ctx context.Context, fileSystem FileSystem, filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if r, ok := w.(RemoveAllFileSystem); ok {
		return r.RemoveAll(ctx, filePath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Lstat, so that a symbolic link is removed
	// and not the files it points to
	info, err := fsLstat(w, filePath)
	if err != nil {
		return RemoveErrDoesNotExist(err)
	}
	return removeAll(ctx, w, filePath, info)
}

// removeAll removes filePath, described by info, and if it is
// a directory everything in it. Symbolic links are removed,
// never followed: ListDir describes a link itself with IsSymlink,
// even if IsDir is set for a link to a directory.
func removeAll(ctx context.Context, w WriteFileSystem, filePath string, info *FileInfo) error {
	if info.IsDir && !info.IsSymlink {
		err := fsListDir(ctx, w, filePath, nil, func(entry *FileInfo) error {
			// Ignore entries that have been deleted concurrently,
			// after all we wanted to get rid of them in the first place
			return RemoveErrDoesNotExist(removeAll(ctx, w, entry.File.Path(), entry))
		})
		if RemoveErrDoesNotExist(err) != nil {
			return err
		}
	}
	return RemoveErrDoesNotExist(w.Remove(filePath))
}

// fsMove moves srcPath to destPath within one file system,
// destPath being the final path, see [MoveFileSystem.Move].
func fsMove(ctx context.Context, fileSystem FileSystem, srcPath, destPath string) error {
	if srcPath == "" || destPath == "" {
		return ErrEmptyPath
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return err
	}
	if srcPath == destPath {
		return nil
	}
	// Checked for native implementations too, a copying
	// implementation would recurse into the copy.
	if isSubPath(fileSystem, srcPath, destPath) {
		return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, fsFile(fileSystem, srcPath), fsFile(fileSystem, destPath))
	}
	if m, ok := w.(MoveFileSystem); ok {
		return m.Move(ctx, srcPath, destPath)
	}
	// Emulate by copying and removing
	return moveByCopy(ctx, fileSystem, srcPath, fileSystem, destPath)
}

// moveByCopy moves srcPath of srcFS to destPath of destFS by copying it
// and removing the source after the copy completely succeeded,
// so a failed or canceled copy never deletes the source.
// The partial copy of a failed directory copy is left at destPath.
//
// destPath is the final path with the overwrite semantics of
// [MoveFileSystem.Move]: an existing file is replaced
// ([ErrIsNotDirectory] if the source is a directory),
// an existing directory is an [ErrAlreadyExists] and neither merged nor deleted.
// An existing file that the copy of a moved file would write into instead of
// replacing it, like a device, a named pipe or a socket, see isSpecialFile,
// is the error of errSpecialFile. A local file that Windows describes as
// irregular is written like a regular file, see isLocalSpecialFile: the
// placeholder of a OneDrive file is replaced, over other reparse points, like
// AppExecLinks or WSL links, the move fails with the error of the write.
// A directory moved into itself within one file system must have been
// rejected by the caller, see fsMove. Local paths are also compared as
// files, because two paths can be the same file, like through bind mounts,
// a symbolic link to a parent directory, a SubFileSystem or the local layer
// of an OverlayFileSystem, see fsLocalPath. Moving a file or directory onto
// itself that way is a no-op, unless the name in the same directory only
// differs in case: then the file is renamed like by LocalFileSystem.Move.
// Moving a directory into itself that way, a file onto another hard link
// of it, and a case-only rename through an OverlayFileSystem, which supports
// only one case of a name, are errors wrapping [os.ErrInvalid].
// On every file system the paths are also compared through SubFileSystems
// and OverlayFileSystems, see isSameTarget: a destPath that a write takes to
// the entry that the source shows, see writeTarget and fsReadTarget, like
// through two SubFileSystems of one parent, is a no-op. Moving an entry of
// the base layer of an overlay to its path in the upper layer, which removing
// the source would remove, and a directory into itself, also into a directory
// that it shows through a SubFileSystem or in a layer of an overlay, see
// isInsideDirView, are errors wrapping [os.ErrInvalid].
//
// A directory is copied to destPath, which didn't exist, so every entry of
// the copy is new, see treeCopier.newDestDir: an existing entry where the
// copy is to write one was written by the copy for another name of the
// source that the destination doesn't tell apart, like "A.txt" and "a.txt"
// of a case-sensitive source on a case-insensitive destination, see destDir,
// or by another process that created destPath after it was checked here,
// maybe long before the copy, because the source is listed first, see
// treeCopier.checkSource. Then the move fails with an error wrapping
// [os.ErrExist] instead of replacing the entry and removing the source of
// the replaced one. A name that is not valid for destFS, see
// treeCopier.validName, is an error wrapping [os.ErrInvalid], below
// a directory, also of an empty directory, before anything is copied, see
// treeCopier.checkSource. So is a name of destPath that the ValidateName
// method of a [ValidateNameFileSystem] rejects, see
// treeCopier.validateWriteName, and one that the local file system would
// not write to an entry of that name or that Windows refuses, see
// isValidLocalName, checked before the parent directories of destPath
// are made.
//
// Within a file system that implements [SymbolicLinkFileSystem],
// symbolic links are copied as links with their target stored verbatim,
// like [os.Rename] keeps them, and a link replaces an existing file or link.
// Between file systems, or within one without symbolic links, a link
// to a file is followed and the file is copied. A link to a directory,
// the moved one or one below a moved directory, can't be copied that way,
// another file system can resolve its target to another directory, see
// CopyRecursive: the move fails with an error wrapping
// [errors.ErrUnsupported] that names the link before anything is copied.
// A symbolic link at destPath is replaced, not the file it points to,
// and recreated if the copy fails. Moving a symbolic link onto the file
// it points to is an error wrapping [os.ErrInvalid], see the checks of the
// existing destPath below, because the file would be replaced with a link
// to itself.
// Removing the source never follows symbolic links, see fsRemoveAll.
// The copied files get permissions like with CopyFile.
func moveByCopy(ctx context.Context, srcFS FileSystem, srcPath string, destFS FileSystem, destPath string) error {
	if srcPath == "" || destPath == "" {
		// Like the one of a file URI without a path, which names no file,
		// also not the working directory, see isSameTarget
		return ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The source must be removable after the copy
	if _, err := fsWritable(srcFS); err != nil {
		return err
	}
	if _, err := fsWritable(destFS); err != nil {
		return err
	}
	srcInfo, err := fsLstat(srcFS, srcPath)
	if err != nil {
		return err
	}
	c := newTreeCopier(ctx, srcFS, destFS, nil)
	// A directory is only copied to a destination that didn't exist, so every
	// existing entry below it is an error, see destDir, also one of another
	// process that creates the destination after the check below
	c.move = true
	if srcFS == destFS {
		// Within one file system every link is moved as link
		c.links = c.dirLinks
	}
	srcIsLink := srcInfo.IsSymlink
	moveLink := srcIsLink && c.links != nil
	if srcIsLink && !moveLink {
		target, err := fsStat(srcFS, srcPath)
		if err != nil {
			return err
		}
		if target.IsDir {
			// A link to a directory is moved as link
			moveLink = true
		} else {
			// Copy the file that the link points to
			srcInfo = target
		}
	}
	srcIsDir := srcInfo.IsDir && !srcInfo.IsSymlink
	src, dest := fsFile(srcFS, srcPath), fsFile(destFS, destPath)
	if _, name := fsSplitDirAndName(destFS, destPath); name != "" {
		// The copy would fail at a name that ValidateName rejects after it made
		// the parent directories, which a zip writer can't remove
		if err := c.validateWriteName(destFS, name); err != nil {
			return errInvalidName(src, name, dest, err)
		}
		// Like the name of a device, the move would write to it
		if c.localDest && !isValidLocalName(name) {
			return errInvalidName(src, name, dest, nil)
		}
	}

	// The entry that the source shows, see fsReadTarget, for all checks
	// below: through an OverlayFileSystem it costs Lstats of the layers
	srcRead, srcReadPath, srcViaOverlay, readOK := fsReadTarget(srcFS, srcPath)
	if _, srcIsLocal := srcRead.(*LocalFileSystem); readOK && srcIsLocal {
		// The local paths of both, like fsLocalPaths tells them
		srcLocal := srcReadPath
		destLocal, destViaOverlay, destIsLocal := fsLocalPath(destFS, destPath)
		if destIsLocal && isLocalSameFile(srcLocal, destLocal, os.Lstat) {
			// The copy would be a no-op and removing
			// the source would remove the only copy
			if isLocalSameDirEntry(srcLocal, destLocal) {
				if filepath.Base(srcLocal) == filepath.Base(destLocal) {
					return nil
				}
				if !srcViaOverlay && !destViaOverlay {
					// Another case of the name, renamed like by LocalFileSystem.Move
					srcLocalInfo, err := os.Lstat(srcLocal)
					if err != nil {
						return wrapOSErr(srcLocal, err)
					}
					return moveLocalSameFile(srcLocal, destLocal, srcLocalInfo)
				}
			}
			// Another hard link of the file, or the same directory entry
			// under a name that only the file system matches, like another
			// Unicode normalization on macOS. Without knowing which,
			// nothing is removed. A layer of an overlay is never renamed
			// past the overlay, which supports only one case of a name.
			return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, errSubject(src), errSubject(dest))
		}
	}
	if srcIsDir && isCopyIntoItself(srcFS, srcPath, srcRead, srcReadPath, destFS, destPath) {
		return fmt.Errorf("%w: can't move directory %s into itself at %s", os.ErrInvalid, errSubject(src), errSubject(dest))
	}
	// Through SubFileSystems and OverlayFileSystems also of other
	// file systems than the local one, compared by path
	if destTarget, destTargetPath, _, ok := writeTarget(destFS, destPath); ok {
		srcWrite, srcWritePath, _, writeOK := writeTarget(srcFS, srcPath)
		switch {
		case readOK && isSameTarget(srcRead, srcReadPath, destTarget, destTargetPath):
			// The copy would be a no-op and removing
			// the source would remove the only copy
			return nil
		case writeOK && isSameTarget(srcWrite, srcWritePath, destTarget, destTargetPath):
			// An entry of the base layer of an overlay moved to its path in
			// the upper layer, where removing the source removes the copy
			return fmt.Errorf("%w: can't move %s to %s, removing the source would remove the moved file", os.ErrInvalid, errSubject(src), errSubject(dest))
		}
	}
	if moveLink && !c.canCopyDirLinks() {
		return c.errDirLink(src, dest)
	}

	// destInfo stays nil if there is nothing to replace
	destInfo, err := fsLstat(destFS, destPath)
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrWriteOnlyFileSystem):
		// Nothing to replace, or a write-only file system that can't tell
	case err != nil:
		return err
	case destInfo.IsDir && !destInfo.IsSymlink:
		return NewErrAlreadyExists(dest)
	case srcIsDir:
		return NewErrIsNotDirectory(dest)
	case !destInfo.IsSymlink:
		if srcIsLink {
			// A move of the link onto the file that it points to would replace
			// the file with the link: the link would then point to itself and
			// the file would be gone, so mv refuses to move them as the same
			// file. Local paths, also of the
			// local layers of an OverlayFileSystem, are compared like by
			// LocalFileSystem.Move, see isLocalLinkOntoTarget, because the local
			// file system can resolve the link to another path than the overlay,
			// a file with other hard links is kept by them and replaced like any
			// other file. Within one other file system the paths are compared
			// with every symbolic link resolved, see fsEvalSymlinks, a link that
			// can't be resolved doesn't point to destPath.
			linkOntoTarget := false
			if srcLocal, destLocal, _, bothLocal := fsLocalPaths(srcFS, srcPath, destFS, destPath); bothLocal {
				destLocalInfo, lstatErr := os.Lstat(destLocal)
				linkOntoTarget = lstatErr == nil && isLocalLinkOntoTarget(srcLocal, destLocalInfo)
			} else if srcFS == destFS {
				if srcResolved, srcResolvedOK := fsEvalSymlinks(srcFS, srcPath); srcResolvedOK {
					destResolved, destResolvedOK := fsEvalSymlinks(destFS, destPath)
					linkOntoTarget = destResolvedOK && srcResolved == destResolved
				}
			}
			if linkOntoTarget {
				return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, errSubject(src), errSubject(dest))
			}
		}
		if !moveLink && isSpecialFile(dest, destInfo) {
			// A link replaces it like os.Rename, a copy would write into it
			return errSpecialFile(src, dest)
		}
	}

	switch {
	case moveLink:
		err = c.replaceWithLink(src, dest, destInfo)
	case srcIsDir:
		err = c.checkSource(src, dest)
		if err == nil {
			err = c.copyDir(src, dest, nil)
		}
	default:
		var restoreLink func() error
		if destInfo != nil && destInfo.IsSymlink {
			// Replace the link, not the file it points to
			restoreLink, err = removeLink(destFS, destPath)
			if err != nil {
				return err
			}
		}
		// The Lstat above told that dest is no directory
		err = copyFileTo(ctx, src, srcInfo.Permissions, dest, &c.buf, nil, true, true)
		if err != nil && restoreLink != nil {
			err = errors.Join(err, restoreLink())
		}
	}
	if err != nil {
		return err
	}
	return fsRemoveAll(ctx, srcFS, srcPath)
}

// evalSymlinksMaxLinks is the number of symbolic links
// that fsEvalSymlinks follows, like filepath.EvalSymlinks.
const evalSymlinksMaxLinks = 255

// fsEvalSymlinks returns the clean filePath of fileSystem with every symbolic
// link in it resolved, like filepath.EvalSymlinks, and false if a link can't
// be read, an element doesn't exist or more than evalSymlinksMaxLinks links
// are followed. Like MemFileSystem resolves its paths, the elements are walked
// from the root and a link is replaced with the elements of its target,
// which is absolute or relative to the resolved directory of the link.
// Every element costs a Lstat, a round trip on a remote file system.
func fsEvalSymlinks(fileSystem FileSystem, filePath string) (string, bool) {
	// The links can only be read with linkTargetReader or with
	// SymbolicLinkFileSystem below all SubFileSystems, see fsLinks
	if _, ok := fileSystem.(linkTargetReader); !ok && fsLinks(fileSystem) == nil {
		return "", false
	}
	sep := fileSystem.Separator()
	filePath = fileSystem.CleanPath(filePath)
	resolved := fileSystem.CleanPath(filePath[:fsVolumeLen(fileSystem, filePath)])
	pending := fsSplitPath(fileSystem, filePath)
	for links := 0; len(pending) > 0; {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			resolved = fileSystem.CleanPath(resolved, "..")
			continue
		}
		next := fileSystem.CleanPath(resolved, name)
		info, err := fsLstat(fileSystem, next)
		if err != nil {
			return "", false
		}
		if !info.IsSymlink {
			resolved = next
			continue
		}
		if links++; links > evalSymlinksMaxLinks {
			return "", false
		}
		// The target as stored, read through one of the
		// interfaces that the check at the top found
		var target string
		switch f := fileSystem.(type) {
		case SymbolicLinkFileSystem:
			target, err = f.ReadSymbolicLink(next)
		case linkTargetReader:
			target, err = f.readSymbolicLink(next)
		}
		if err != nil || target == "" {
			return "", false
		}
		volumeLen := fsVolumeLen(fileSystem, target)
		if volumeLen > 0 || strings.HasPrefix(target, "/") || strings.HasPrefix(target, sep) {
			// An absolute target
			resolved = fileSystem.CleanPath(target[:volumeLen])
		}
		elems := strings.Split(strings.ReplaceAll(target[volumeLen:], sep, "/"), "/")
		pending = append(elems, pending...)
	}
	return resolved, true
}

// isSubPath reports if filePath is inside the directory dirPath
// of fileSystem, not counting dirPath itself.
// A path without a volume name is on the same volume
// as a path with one.
func isSubPath(fileSystem FileSystem, dirPath, filePath string) bool {
	dirVolume := dirPath[:fsVolumeLen(fileSystem, dirPath)]
	fileVolume := filePath[:fsVolumeLen(fileSystem, filePath)]
	if dirVolume != "" && fileVolume != "" && dirVolume != fileVolume {
		return false
	}
	dir := fsSplitPath(fileSystem, dirPath)
	file := fsSplitPath(fileSystem, filePath)
	return len(file) > len(dir) && slices.Equal(dir, file[:len(dir)])
}

// isValidName reports if name can be the name of a directory entry
// of fileSystem: not empty, not "." or ".." and without a separator.
// A slash is a separator for every file system,
// because CleanPath treats it as one.
func isValidName(fileSystem FileSystem, name string) bool {
	switch {
	case name == "", name == ".", name == "..":
		return false
	case strings.Contains(name, "/"), strings.Contains(name, fileSystem.Separator()):
		return false
	}
	return true
}

// validateWriteName returns an error if fileSystem can't write an entry with
// name: os.ErrInvalid for a name that is not valid, see isValidName, and
// the error of the ValidateName method of a ValidateNameFileSystem that rejects
// it, which tells why. Existing entries are read and removed with every
// name of isValidName.
func validateWriteName(fileSystem FileSystem, name string) error {
	if !isValidName(fileSystem, name) {
		return os.ErrInvalid
	}
	if v, ok := fileSystem.(ValidateNameFileSystem); ok {
		return v.ValidateName(name)
	}
	return nil
}

// validateWritePathName returns the name of filePath and the error of
// validateWriteName for it, of fileSystem and of the file system that the
// writes to filePath go to, see writeTarget. A path without a name,
// like the root, is no error.
func validateWritePathName(fileSystem FileSystem, filePath string) (name string, err error) {
	_, name = fsSplitDirAndName(fileSystem, filePath)
	if name == "" {
		return "", nil
	}
	if err := validateWriteName(fileSystem, name); err != nil {
		return name, err
	}
	if target, _, _, ok := writeTarget(fileSystem, filePath); ok && target != fileSystem {
		return name, validateWriteName(target, name)
	}
	return name, nil
}

func fsListDirRecursive(ctx context.Context, fileSystem FileSystem, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	if err := fsReadable(fileSystem); err != nil {
		return err
	}
	if l, ok := fileSystem.(ListDirRecursiveFileSystem); ok {
		return l.ListDirRecursive(ctx, dirPath, patterns, func(info *FileInfo) error {
			return callback(completeFileInfo(fileSystem, dirPath, info))
		})
	}
	err := listDirRecursive(ctx, fileSystem, dirPath, patterns, func(info *FileInfo) error {
		if err := callback(info); err != nil {
			return callbackError{err}
		}
		return nil
	})
	var cbErr callbackError
	if errors.As(err, &cbErr) {
		return cbErr.err
	}
	return err
}

// callbackError marks an error returned by the callback of a listing,
// so that it can be told apart from the errors of the listing itself.
type callbackError struct{ err error }

func (e callbackError) Error() string { return e.err.Error() }
func (e callbackError) Unwrap() error { return e.err }

// listDirRecursive emulates ListDirRecursive with ListDir.
// The callback must return its errors as callbackError.
//
// Symbolic links are listed like files and never followed, also a link
// to a directory, which is passed with IsDir false, like by filepath.WalkDir.
// Only a link at dirPath is followed, because ListDir follows it.
func listDirRecursive(ctx context.Context, fileSystem FileSystem, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	return fsListDir(ctx, fileSystem, dirPath, nil, func(info *FileInfo) error {
		if info.IsDir && !info.IsSymlink {
			// Not returning directories, but recursing into them.
			err := listDirRecursive(ctx, fileSystem, info.File.Path(), patterns, callback)
			if errors.As(err, new(callbackError)) {
				return err
			}
			// Don't mind directories that have been deleted while iterating,
			// but never ignore an error of the callback.
			return RemoveErrDoesNotExist(err)
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
		if !match || err != nil {
			return err
		}
		// ListDir describes a link to a directory with IsDir set
		// if the file system knows its target cheaply
		info.IsDir = false
		return callback(info)
	})
}

///////////////////////////////////////////////////////////////////////////////
// Symbolic links and local paths for copies and moves

// linkTargetReader is implemented by file systems that can read the target
// of a symbolic link without implementing SymbolicLinkFileSystem, like
// OverlayFileSystem with the links of its layers, so that fsEvalSymlinks
// can resolve their links.
type linkTargetReader interface {
	readSymbolicLink(linkPath string) (string, error)
}

// fsLinks returns fileSystem as SymbolicLinkFileSystem if it implements it,
// else nil. A SubFileSystem implements it for every parent, so it is
// only returned if the parent below all SubFileSystems implements it.
func fsLinks(fileSystem FileSystem) SymbolicLinkFileSystem {
	links, ok := fileSystem.(SymbolicLinkFileSystem)
	if !ok {
		return nil
	}
	for parent := fileSystem; ; {
		sub, isSub := parent.(*SubFileSystem)
		if !isSub {
			if _, ok := parent.(SymbolicLinkFileSystem); !ok {
				return nil
			}
			return links
		}
		parent = sub.parent
	}
}

// fsLocalPaths returns the local paths of aPath of aFS and bPath of bFS
// and true if both are paths of the local file system, see fsLocalPath.
// viaOverlay is true if one of them is a path of an OverlayFileSystem.
func fsLocalPaths(aFS FileSystem, aPath string, bFS FileSystem, bPath string) (aLocal, bLocal string, viaOverlay, ok bool) {
	aLocal, aOverlay, aOK := fsLocalPath(aFS, aPath)
	bLocal, bOverlay, bOK := fsLocalPath(bFS, bPath)
	return aLocal, bLocal, aOverlay || bOverlay, aOK && bOK
}

// fsLocalPath returns the local path of filePath of fileSystem and true
// if it is a path of the local file system, directly or through
// SubFileSystems and OverlayFileSystems, see fsReadTarget. An
// OverlayFileSystem is unwrapped to the layer that holds the path, see
// OverlayFileSystem.pathLayer, and viaOverlay is true then: the local path
// may only be used to compare files, because a write past the overlay would
// change its read-only base layer or bring back the base entries that it hides.
func fsLocalPath(fileSystem FileSystem, filePath string) (localPath string, viaOverlay, ok bool) {
	target, targetPath, viaOverlay, ok := fsReadTarget(fileSystem, filePath)
	if _, local := target.(*LocalFileSystem); !ok || !local {
		return "", false, false
	}
	return targetPath, viaOverlay, true
}

// fsReadTarget returns the file system and the path of the entry that
// filePath of fileSystem shows, through SubFileSystems to their parent and
// through OverlayFileSystems to the layer that holds the path, see
// OverlayFileSystem.pathLayer, which costs a Lstat of the upper layer and,
// for a path that the upper layer has no entry for, one of the base layer.
// viaOverlay tells that an OverlayFileSystem was passed: then a write to
// filePath can go to another entry, the one of writeTarget in the upper
// layer. ok is false for a path of a SubFileSystem that has no path in its
// parent and for a path of an OverlayFileSystem whose layer can't be told.
func fsReadTarget(fileSystem FileSystem, filePath string) (target FileSystem, targetPath string, viaOverlay, ok bool) {
	for {
		switch f := fileSystem.(type) {
		case *SubFileSystem:
			parentPath, err := f.parentPath(filePath)
			if err != nil {
				return nil, "", false, false
			}
			fileSystem, filePath = f.parent, parentPath
		case *OverlayFileSystem:
			layer, layerPath, err := f.pathLayer(filePath)
			if err != nil {
				return nil, "", false, false
			}
			fileSystem, filePath, viaOverlay = layer, layerPath, true
		default:
			return fileSystem, filePath, viaOverlay, true
		}
	}
}

// isLocalSameFile reports if the local paths a and b are the same file
// or directory, like two paths through bind mounts or through a symbolic
// link to a parent directory, or two hard links of a file.
// stat is os.Stat to compare what symbolic links at a and b point to,
// or os.Lstat to compare the links themselves.
func isLocalSameFile(a, b string, stat func(string) (os.FileInfo, error)) bool {
	aInfo, err := stat(a)
	if err != nil {
		return false
	}
	bInfo, err := stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

// isLocalSameDirEntry reports if the local paths a and b of the same file
// have the same name in the same directory. The names are compared with
// strings.EqualFold, so a false result can still be the same directory entry
// for a file system that matches names by Unicode normalization or with
// other case rules, never treat it as proof of two different entries.
func isLocalSameDirEntry(a, b string) bool {
	return strings.EqualFold(filepath.Base(a), filepath.Base(b)) && isLocalInSameDir(a, b)
}

// isLocalInSameDir reports if the local paths a and b are in the same directory.
func isLocalInSameDir(a, b string) bool {
	aDir, err := os.Stat(filepath.Dir(a))
	if err != nil {
		return false
	}
	bDir, err := os.Stat(filepath.Dir(b))
	if err != nil {
		return false
	}
	return os.SameFile(aDir, bDir)
}

// isLocalInsideDir reports if the local path filePath is inside the directory
// dirPath, also through another path to the directory, like through a bind
// mount or a symbolic link: one of the parent directories of filePath is
// the same directory as dirPath. The empty path names no file, see
// isSameTarget, filepath.Abs would make it the working directory.
func isLocalInsideDir(dirPath, filePath string) bool {
	if dirPath == "" || filePath == "" {
		return false
	}
	dirInfo, err := os.Stat(dirPath)
	if err != nil {
		return false
	}
	filePath, err = filepath.Abs(filePath)
	if err != nil {
		return false
	}
	for parent := filepath.Dir(filePath); ; parent = filepath.Dir(parent) {
		if info, err := os.Stat(parent); err == nil && os.SameFile(dirInfo, info) {
			return true
		}
		if filepath.Dir(parent) == parent {
			return false
		}
	}
}
