package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	prefix, sep := fileSystem.Prefix(), fileSystem.Separator()
	if strings.HasSuffix(prefix, sep) && strings.HasPrefix(cleanPath, sep) {
		return prefix + cleanPath[len(sep):]
	}
	return prefix + cleanPath
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
	return ReadAllContext(ctx, reader)
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
	err = WriteAllContext(ctx, writer, data)
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
		err = WriteAllContext(ctx, writer, data)
		return errors.Join(err, writer.Close())
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
		return t.Truncate(filePath, size)
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
	info, err := w.Stat(filePath)
	if err != nil {
		return RemoveErrDoesNotExist(err)
	}
	if info.IsDir {
		err = fsRemoveDirContents(ctx, w, filePath)
		if err != nil {
			return err
		}
	}
	return RemoveErrDoesNotExist(w.Remove(filePath))
}

// fsRemoveDirContents removes all entries of dirPath recursively.
func fsRemoveDirContents(ctx context.Context, w WriteFileSystem, dirPath string) error {
	return fsListDir(ctx, w, dirPath, nil, func(info *FileInfo) error {
		// Ignore entries that have been deleted concurrently,
		// after all we wanted to get rid of them in the first place
		return RemoveErrDoesNotExist(fsRemoveAll(ctx, w, info.File.Path()))
	})
}

// fsMove moves srcPath to destPath within one file system,
// destPath being the final path.
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
	if m, ok := w.(MoveFileSystem); ok {
		return m.Move(srcPath, destPath)
	}
	// Emulate by copying and removing
	src, dest := fsFile(fileSystem, srcPath), fsFile(fileSystem, destPath)
	err = CopyRecursive(ctx, src, dest)
	if err != nil {
		return err
	}
	return fsRemoveAll(ctx, fileSystem, srcPath)
}

func fsRename(fileSystem FileSystem, filePath, newName string) (newPath string, err error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	if strings.ContainsAny(newName, fileSystem.Separator()) {
		return "", fmt.Errorf("newName %#v for Rename contains path separator %s", newName, fileSystem.Separator())
	}
	w, err := fsWritable(fileSystem)
	if err != nil {
		return "", err
	}
	if r, ok := w.(RenameFileSystem); ok {
		return r.Rename(filePath, newName)
	}
	dir, _ := fsSplitDirAndName(fileSystem, filePath)
	newPath = fileSystem.CleanPath(dir, newName)
	return newPath, fsMove(context.Background(), fileSystem, filePath, newPath)
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
	if l, ok := fileSystem.(ListDirMaxFileSystem); ok {
		return l.ListDirMax(ctx, dirPath, max, patterns)
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
	return fsListDir(ctx, fileSystem, dirPath, nil, func(info *FileInfo) error {
		if info.IsDir {
			// Not returning directories, but recursing into them.
			// Don't mind directories that have been deleted while iterating.
			return RemoveErrDoesNotExist(fsListDirRecursive(ctx, fileSystem, info.File.Path(), patterns, callback))
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
		if !match || err != nil {
			return err
		}
		return callback(info)
	})
}
