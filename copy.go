package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ungerik/go-fs/fsimpl"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file to dest, which can be on another file system.
//
// If dest is an existing directory, then a file with the name of src
// is created in it, else dest is the path of the copy.
// An existing file at that path is replaced, an existing directory
// is an [ErrIsDirectory]. Missing parent directories of the copy are created.
// Copying a file onto itself is a no-op.
// src must exist and must not be a directory ([ErrIsDirectory]).
//
// The copy is created with perm, or without perm with the permissions
// of src if it is a File. Like for [File.OpenWriter], perm applies to
// a newly created file, file systems may keep the permissions of a replaced one.
// Within one file system that implements [CopyFileSystem], its native
// CopyFile is used. If perm is passed, it is then applied to the copy
// with [PermissionsFileSystem.SetPermissions], and file systems that don't
// implement PermissionsFileSystem copy the data through [File.OpenWriter] with perm.
//
// The copied data is only committed if the copy succeeded:
// when reading the source fails or ctx is canceled, a destination writer
// that implements [WriteAborter] is aborted, so an upload doesn't
// replace an existing file with partial data.
// The error of closing the destination writer, like a failed upload,
// is returned.
func CopyFile(ctx context.Context, src FileReader, dest File, perm ...Permissions) error {
	return CopyFileBuf(ctx, src, dest, nil, perm...)
}

// CopyFileBuf copies a single file like [CopyFile],
// using buf as copy buffer like [io.CopyBuffer].
// If buf is empty, a buffer is allocated.
// Use CopyFileBuf to re-use a buffer for several copies.
func CopyFileBuf(ctx context.Context, src FileReader, dest File, buf []byte, perm ...Permissions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dest == "" {
		return ErrEmptyPath
	}
	if dest.IsDir() {
		dest = dest.Join(src.Name())
	}
	return copyFileFinal(ctx, src, dest, buf, perm)
}

// copyFileFinal copies src to dest like CopyFileBuf,
// but dest is always the final path of the copy:
// an existing directory at dest is an ErrIsDirectory
// instead of the directory to copy into.
// FileSystem implementations use it for CopyFileSystem.CopyFile
// with files of another file system.
func copyFileFinal(ctx context.Context, src FileReader, dest File, buf []byte, perm []Permissions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dest == "" {
		return ErrEmptyPath
	}
	srcPerm, err := checkCopySource(src)
	if err != nil {
		return err
	}
	return copyFileTo(ctx, src, srcPerm, dest, &buf, perm, true)
}

// checkCopySource returns an error if src does not exist or is a directory,
// and the permissions of src if it is a File.
// A MemFile always exists, its name doesn't matter for its data.
func checkCopySource(src FileReader) (Permissions, error) {
	switch f := src.(type) {
	case File:
		fileSystem, path := f.ParseRawURI()
		info, err := fsStat(fileSystem, path)
		if err != nil {
			return NoPermissions, err
		}
		if info.IsDir {
			return NoPermissions, NewErrIsDirectory(f)
		}
		return info.Permissions, nil
	case MemFile:
		if f.IsDir() {
			return NoPermissions, NewErrIsDirectory(f)
		}
		return NoPermissions, nil
	}
	if err := src.CheckExists(); err != nil {
		return NoPermissions, err
	}
	if src.IsDir() {
		return NoPermissions, NewErrIsDirectory(src)
	}
	return NoPermissions, nil
}

// copyFileTo copies the checked source file src to dest,
// dest being the final path of the copy: an existing file
// is replaced, an existing directory is an ErrIsDirectory.
// Without perm the copy gets srcPerm.
// The parent directories of dest are created if makeParentDirs is true.
// buf points to the copy buffer, which is allocated if empty.
func copyFileTo(ctx context.Context, src FileReader, srcPerm Permissions, dest File, buf *[]byte, perm []Permissions, makeParentDirs bool) error {
	destFS, destPath := dest.ParseRawURI()
	if destPath == "" {
		return ErrEmptyPath
	}
	if _, err := fsWritable(destFS); err != nil {
		return err
	}
	srcFile, srcIsFile := src.(File)
	var srcFS FileSystem
	var srcPath string
	if srcIsFile {
		srcFS, srcPath = srcFile.ParseRawURI()
		if srcFS == destFS && srcPath == destPath {
			return nil // Copying a file onto itself is a no-op
		}
	}
	if dest.IsDir() {
		return NewErrIsDirectory(dest)
	}
	if makeParentDirs {
		destDir := dest.Dir()
		if err := destDir.MakeAllDirs(); err != nil {
			return fmt.Errorf("CopyFile: can't make directory %q: %w", destDir, err)
		}
	}

	setPerm := JoinPermissions(perm, NoPermissions)
	if srcIsFile && srcFS == destFS {
		copyFS, canCopy := destFS.(CopyFileSystem)
		permFS, canSetPerm := destFS.(PermissionsFileSystem)
		if canCopy && (setPerm == NoPermissions || canSetPerm) {
			if err := copyFS.CopyFile(ctx, srcPath, destPath); err != nil {
				return err
			}
			if setPerm == NoPermissions {
				return nil
			}
			return permFS.SetPermissions(destPath, setPerm)
		}
	}

	filePerm := setPerm.OrDefault(srcPerm)
	if m, ok := src.(MemFile); ok {
		// No copy buffer needed for data in memory
		return fsWriteAll(ctx, destFS, destPath, m.FileData, filePerm)
	}
	r, err := src.OpenReader()
	if err != nil {
		return fmt.Errorf("CopyFile: can't open source reader: %w", err)
	}
	defer r.Close()
	w, err := fsOpenWriter(destFS, destPath, filePerm)
	if err != nil {
		return fmt.Errorf("CopyFile: can't open destination writer: %w", err)
	}
	if len(*buf) == 0 {
		*buf = make([]byte, copyBufferSize)
	}
	return commitOrAbort(ctx, w, copyBuffer(ctx, w, r, *buf))
}

func copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (err error) {
	for err = ctx.Err(); err == nil; err = ctx.Err() {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errors.New("invalid write result")
				}
			}
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return nil
			}
			return er
		}
	}
	return err
}

// CopyRecursive copies src to dest, which can be on another file system.
//
// If src is a file, it is copied like by [CopyFile].
//
// If src is a directory, its files and sub-directories are copied
// recursively to the directory dest, which is created if it does not exist
// and merged with if it does: a file of src replaces a file at the same path
// in dest, a file of src over a directory of dest is an [ErrIsDirectory]
// and a directory of src over a file of dest an [ErrIsNotDirectory].
// Copying a directory onto itself is a no-op,
// copying it into one of its sub-directories is an error wrapping [os.ErrInvalid].
//
// The filter patterns are applied to the names of files,
// sub-directories are always copied, like for [File.ListDirRecursive].
// Symbolic links are followed: the copy contains what they point to.
// An entry of src with a name that is not valid for dest,
// like the name `..\x` from an archive on a file system with the separator `\`,
// is an error wrapping [os.ErrInvalid], so no file can be written outside of dest.
// One copy buffer is used for all files.
func CopyRecursive(ctx context.Context, src, dest File, patterns ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dest == "" {
		return ErrEmptyPath
	}
	if !src.IsDir() {
		// Just copy one file
		return CopyFile(ctx, src, dest)
	}
	srcFS, srcPath := src.ParseRawURI()
	destFS, destPath := dest.ParseRawURI()
	if srcFS == destFS {
		if srcPath == destPath {
			return nil // Copying a directory onto itself is a no-op
		}
		if isSubPath(srcFS, srcPath, destPath) {
			return fmt.Errorf("%w: can't copy directory %s into itself at %s", os.ErrInvalid, src, dest)
		}
	}
	c := treeCopier{ctx: ctx, patterns: patterns}
	return c.copyDir(src, dest)
}

// treeCopier copies directory trees for CopyRecursive and moveByCopy.
type treeCopier struct {
	ctx      context.Context
	patterns []string               // only files with a matching name are copied
	links    SymbolicLinkFileSystem // if not nil, symbolic links are copied as links within it
	buf      []byte                 // copy buffer for all files, allocated on first use
}

// copyDir copies the directory src recursively to dest,
// creating dest if it does not exist and merging with it if it does.
func (c *treeCopier) copyDir(src, dest File) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	destFS, destPath := dest.ParseRawURI()
	info, err := fsStat(destFS, destPath)
	switch {
	case err == nil && !info.IsDir:
		return fmt.Errorf("can't copy directory %s over a file: %w", src, NewErrIsNotDirectory(dest))
	case err != nil:
		// Also tried if Stat failed for another reason than a missing
		// directory, like on write-only file systems.
		if err := fsMakeAllDirs(destFS, destPath, NoPermissions); err != nil {
			return fmt.Errorf("can't make directory %q: %w", dest, err)
		}
	}

	srcFS, srcPath := src.ParseRawURI()
	return fsListDir(c.ctx, srcFS, srcPath, nil, func(info *FileInfo) error {
		if !isValidName(destFS, info.Name) {
			return fmt.Errorf("%w: can't copy %s with the name %q to %s", os.ErrInvalid, info.File, info.Name, dest)
		}
		file, fileDest := info.File, dest.Join(info.Name)
		if info.IsSymlink && c.links != nil {
			return c.copyLink(file, fileDest)
		}
		isDir, perm := info.IsDir, info.Permissions
		if info.IsSymlink {
			// Copy what the link points to
			target, err := fsStat(srcFS, file.Path())
			if err != nil {
				return err
			}
			isDir, perm = target.IsDir, target.Permissions
		}
		if isDir {
			return c.copyDir(file, fileDest)
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, c.patterns)
		if err != nil || !match {
			return err
		}
		return copyFileTo(c.ctx, file, perm, fileDest, &c.buf, nil, false)
	})
}

// copyLink copies the symbolic link src
// as a link with the same target to dest.
func (c *treeCopier) copyLink(src, dest File) error {
	target, err := c.links.ReadSymbolicLink(src.Path())
	if err != nil {
		return err
	}
	return c.links.CreateSymbolicLink(target, dest.Path())
}
