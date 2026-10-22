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

// fsURL returns the URI of a clean path within fileSystem.
func fsURL(fileSystem FileSystem, cleanPath string) string {
	if local, ok := fileSystem.(*LocalFileSystem); ok {
		return local.URL(cleanPath)
	}
	if u, ok := fileSystem.(URIFileSystem); ok {
		return u.URIFromPath(cleanPath)
	}
	// Same rule as PathHelper.URL: a prefix ending with the separator
	// after a host doesn't double it, a scheme-only prefix like "x://"
	// is followed by the whole rooted path
	return fsimpl.PathHelper{URIPrefix: fileSystem.Prefix(), PathSep: fileSystem.Separator()}.URL(cleanPath)
}

// fsFile returns the File for a clean path within fileSystem.
// Files of the local file system have no prefix.
func fsFile(fileSystem FileSystem, cleanPath string) File {
	if _, ok := fileSystem.(*LocalFileSystem); ok {
		return File(cleanPath)
	}
	if _, ok := fileSystem.(InvalidFileSystem); ok && cleanPath == "" {
		return InvalidFile
	}
	return File(fsURL(fileSystem, cleanPath))
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

// fsIsAbsPath returns if filePath is absolute.
// Paths are always absolute except for AbsPathFileSystem implementations.
func fsIsAbsPath(fileSystem FileSystem, filePath string) bool {
	if a, ok := fileSystem.(AbsPathFileSystem); ok {
		return a.IsAbsPath(filePath)
	}
	return true
}

// fsAbsPath returns filePath in absolute form.
func fsAbsPath(fileSystem FileSystem, filePath string) string {
	if a, ok := fileSystem.(AbsPathFileSystem); ok {
		return a.AbsPath(filePath)
	}
	return filePath
}

// fsRelPath returns the path of targPath relative to basePath.
func fsRelPath(fileSystem FileSystem, basePath, targPath string) (string, error) {
	if a, ok := fileSystem.(AbsPathFileSystem); ok {
		return a.RelPath(basePath, targPath)
	}
	sep := fileSystem.Separator()
	base := fsSplitPath(fileSystem, basePath)
	targ := fsSplitPath(fileSystem, targPath)
	common := 0
	for common < len(base) && common < len(targ) && base[common] == targ[common] {
		common++
	}
	var parts []string
	for range base[common:] {
		parts = append(parts, "..")
	}
	parts = append(parts, targ[common:]...)
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, sep), nil
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
	if m, ok := w.(MakeAllDirsFileSystem); ok {
		return m.MakeAllDirs(dirPath, perm)
	}
	return makeAllDirs(w, dirPath, perm)
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
// A directory moved into itself within one file system must have been
// rejected by the caller, see fsMove. Local paths are also compared as
// files, because two paths can be the same file, like through bind mounts,
// a symbolic link to a parent directory or a SubFileSystem: moving a file
// or directory onto itself that way is a no-op, unless the name in the same
// directory only differs in case, then the file is renamed like by
// LocalFileSystem.Move, and moving a directory into itself that way
// is an error wrapping [os.ErrInvalid].
//
// Within a file system that implements [SymbolicLinkFileSystem],
// symbolic links are copied as links with their target stored verbatim,
// like [os.Rename] keeps them, and a link replaces an existing file or link.
// Between file systems, or within one without symbolic links, a link
// to a file is followed and the file is copied. A link to a directory,
// also below a moved directory, is copied as link if destFS can create links
// and, between file systems, its target is relative and stays inside srcPath,
// see CopyRecursive, so a moved link to a directory itself is never copied
// to another file system. Else the move fails with an error wrapping
// [errors.ErrUnsupported] before anything is copied.
// A symbolic link at destPath is replaced, not the file it points to,
// and recreated if the copy fails. Moving a symbolic link onto the file
// it points to is an error wrapping [os.ErrInvalid], see isLinkOntoTarget,
// because the file would be replaced with a link to itself.
// Removing the source never follows symbolic links, see fsRemoveAll.
// The copied files get permissions like with CopyFile.
func moveByCopy(ctx context.Context, srcFS FileSystem, srcPath string, destFS FileSystem, destPath string) error {
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
	c := newTreeCopier(ctx, srcFS, srcPath, destFS, nil)
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

	if srcLocal, destLocal, ok := fsLocalPaths(srcFS, srcPath, destFS, destPath); ok {
		switch {
		case isLocalSameFile(srcLocal, destLocal, os.Lstat):
			// The copy would be a no-op and removing
			// the source would remove the only copy
			if isLocalSameDirEntry(srcLocal, destLocal) {
				if filepath.Base(srcLocal) == filepath.Base(destLocal) {
					return nil
				}
				// Another case of the name, renamed like by LocalFileSystem.Move
				srcLocalInfo, err := os.Lstat(srcLocal)
				if err != nil {
					return wrapOSErr(srcLocal, err)
				}
				return moveLocalSameFile(srcLocal, destLocal, srcLocalInfo)
			}
			// Another hard link of the file, or the same directory entry
			// under a name that only the file system matches, like another
			// Unicode normalization on macOS. Without knowing which,
			// nothing is removed.
			return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, errSubject(src), errSubject(dest))
		case srcIsDir && isLocalInsideDir(srcLocal, destLocal):
			return fmt.Errorf("%w: can't move directory %s into itself at %s", os.ErrInvalid, errSubject(src), errSubject(dest))
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
	case srcIsLink && !destInfo.IsSymlink && isLinkOntoTarget(srcFS, srcPath, destFS, destPath):
		return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, errSubject(src), errSubject(dest))
	}

	switch {
	case moveLink:
		err = c.replaceWithLink(src, dest, destInfo)
	case srcIsDir:
		err = c.checkDirLinks(src, dest)
		if err == nil {
			err = c.copyDir(src, dest, true)
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

// isLinkOntoTarget reports if the symbolic link srcPath of srcFS points to
// destPath of destFS, an existing file that is no link, which a move of the
// link would replace with the link: the link would then point to itself and
// the file would be gone, so mv refuses to move them as the same file.
// Local paths are compared like by LocalFileSystem.Move, see
// isLocalLinkOntoTarget, a file with other hard links is kept by them and
// replaced like any other file. Within one other file system the paths are
// compared with every symbolic link resolved, see fsEvalSymlinks, a link
// that can't be resolved is reported as not pointing to destPath.
func isLinkOntoTarget(srcFS FileSystem, srcPath string, destFS FileSystem, destPath string) bool {
	if srcLocal, destLocal, ok := fsLocalPaths(srcFS, srcPath, destFS, destPath); ok {
		destInfo, err := os.Lstat(destLocal)
		return err == nil && isLocalLinkOntoTarget(srcLocal, destInfo)
	}
	if srcFS != destFS {
		return false
	}
	srcResolved, ok := fsEvalSymlinks(srcFS, srcPath)
	if !ok {
		return false
	}
	destResolved, ok := fsEvalSymlinks(destFS, destPath)
	return ok && srcResolved == destResolved
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
	if !fsCanReadLinks(fileSystem) {
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
		target, err := fsReadSymbolicLink(fileSystem, next)
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

func fsRename(ctx context.Context, fileSystem FileSystem, filePath, newName string) (newPath string, err error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	if !isValidName(fileSystem, newName) {
		return "", fmt.Errorf("%w: invalid new name %q for Rename", os.ErrInvalid, newName)
	}
	dir, _ := fsSplitDirAndName(fileSystem, filePath)
	newPath = fileSystem.CleanPath(dir, newName)
	return newPath, fsMove(ctx, fileSystem, filePath, newPath)
}

func fsListDirMax(ctx context.Context, fileSystem FileSystem, dirPath string, max int, patterns []string) (files []File, err error) {
	if dirPath == "" {
		return nil, ErrEmptyPath
	}
	if max == 0 {
		return nil, nil
	}
	if err := fsReadable(fileSystem); err != nil {
		return nil, err
	}
	return listDirMaxImpl(ctx, max, func(ctx context.Context, callback func(File) error) error {
		return fsListDir(ctx, fileSystem, dirPath, patterns, FileInfoToFileCallback(callback))
	})
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
// OverlayFileSystem with the links of its layers, so that copies can
// recreate their links, see fsReadSymbolicLink.
type linkTargetReader interface {
	readSymbolicLink(linkPath string) (string, error)
}

// fsReadSymbolicLink returns the target of the symbolic link at linkPath
// as stored, using SymbolicLinkFileSystem or linkTargetReader.
func fsReadSymbolicLink(fileSystem FileSystem, linkPath string) (string, error) {
	switch f := fileSystem.(type) {
	case SymbolicLinkFileSystem:
		return f.ReadSymbolicLink(linkPath)
	case linkTargetReader:
		return f.readSymbolicLink(linkPath)
	}
	return "", NewErrUnsupported(fileSystem, "ReadSymbolicLink")
}

// fsCanReadLinks reports if fsReadSymbolicLink
// can read the symbolic links of fileSystem.
func fsCanReadLinks(fileSystem FileSystem) bool {
	if _, ok := fileSystem.(linkTargetReader); ok {
		return true
	}
	return fsLinks(fileSystem) != nil
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
// and true if both are paths of the local file system,
// directly or through SubFileSystems.
func fsLocalPaths(aFS FileSystem, aPath string, bFS FileSystem, bPath string) (aLocal, bLocal string, ok bool) {
	aLocal, aOK := fsLocalPath(aFS, aPath)
	bLocal, bOK := fsLocalPath(bFS, bPath)
	return aLocal, bLocal, aOK && bOK
}

// fsLocalPath returns the local path of filePath of fileSystem and true
// if it is a path of the local file system, directly or through SubFileSystems.
func fsLocalPath(fileSystem FileSystem, filePath string) (string, bool) {
	for {
		switch f := fileSystem.(type) {
		case *LocalFileSystem:
			return filePath, true
		case *SubFileSystem:
			parentPath, err := f.parentPath(filePath)
			if err != nil {
				return "", false
			}
			fileSystem, filePath = f.parent, parentPath
		default:
			return "", false
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
	if !strings.EqualFold(filepath.Base(a), filepath.Base(b)) {
		return false
	}
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
// the same directory as dirPath.
func isLocalInsideDir(dirPath, filePath string) bool {
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
