package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file to dest, which can be on another file system.
//
// If dest is an existing directory, then a file with the name of src
// is created in it, else dest is the path of the copy.
// An existing file at that path is replaced, an existing directory
// is an [ErrIsDirectory]. Missing parent directories of the copy are created.
// Copying a file onto itself is a no-op, for a local file also through
// another path to it, like a symbolic link, a hard link, a [SubFileSystem]
// or the local layer of an [OverlayFileSystem]. dest is checked by path
// before it is opened for writing, which follows a symbolic link at dest,
// so these guarantees assume that dest is not changed concurrently by others.
// src must exist and must not be a directory ([ErrIsDirectory]).
//
// The copy is created with perm, or without perm with the permissions
// of src if it is a File: only its read, write and execute bits, never
// the setuid, setgid and sticky bits, which are only set if passed with perm.
// A src of a file system that is not writable, like an upload or an archive
// entry, lacks the write permissions because of its file system, so its copy
// also gets the write permission of the owner. Like for [File.OpenWriter], perm
// applies to a newly created file, file systems may keep the permissions of a
// replaced one. Within one file system that implements [CopyFileSystem], its
// native CopyFile is used. If perm is passed, it is then applied to the copy
// with [PermissionsFileSystem.SetPermissions], and file systems that don't
// implement PermissionsFileSystem copy the data through [File.OpenWriter] with perm.
//
// When reading the source fails or ctx is canceled, a destination writer
// that implements [WriteAborter] is aborted, so an upload doesn't
// replace an existing file with partial data. Writers without Abort,
// like the ones of local, in-memory, SFTP and SMB files, write in place,
// so a failed copy can leave the destination partial.
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
	// If dest is no directory, copyFileTo doesn't have to check it again,
	// every Stat is a round trip on a remote file system
	destIsDir := dest.IsDir()
	if destIsDir {
		dest = dest.Join(src.Name())
	}
	srcPerm, err := checkCopySource(src)
	if err != nil {
		return err
	}
	return copyFileTo(ctx, src, srcPerm, dest, &buf, perm, true, !destIsDir)
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
	return copyFileTo(ctx, src, srcPerm, dest, &buf, perm, true, false)
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
// Without perm the copy gets the permissions srcPerm of src,
// as far as copyPermissions takes them over.
// The parent directories of dest are created if makeParentDirs is true.
// destIsNotDir tells that the caller already checked
// that dest is no directory, so it is not checked again.
// buf points to the copy buffer, which is allocated if empty.
func copyFileTo(ctx context.Context, src FileReader, srcPerm Permissions, dest File, buf *[]byte, perm []Permissions, makeParentDirs, destIsNotDir bool) error {
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
		// The same local file through another path, like a symbolic link,
		// a hard link, a SubFileSystem or the layer of an OverlayFileSystem,
		// else the writer would truncate the file that is read. Also within
		// one file system, because an OverlayFileSystem resolves the links
		// of its layers to other paths than the local file system.
		srcLocal, destLocal, _, local := fsLocalPaths(srcFS, srcPath, destFS, destPath)
		if local && isLocalSameFile(srcLocal, destLocal, os.Stat) {
			return nil
		}
		srcPerm = copyPermissions(srcFS, srcPerm)
	}
	if !destIsNotDir && dest.IsDir() {
		return NewErrIsDirectory(dest)
	}
	if makeParentDirs {
		destDir := dest.Dir()
		if err := destDir.MakeAllDirs(); err != nil {
			return fmt.Errorf("CopyFile: can't make directory %q: %w", errSubject(destDir), err)
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

// copyPermissions returns the permissions that a copy without perm takes
// over from a source file with the permissions srcPerm on srcFS.
//
// These are only the read, write and execute bits: the setuid, setgid and
// sticky bits are never copied, else extracting an archive as root would
// create setuid files. A file of a file system that is not writable, like an
// upload or an archive entry, lacks the write permissions because of the file
// system, so the copy gets the write permission of the owner, else the copy
// of an upload could not be written. NoPermissions is returned if no bit is
// left, so that the copy gets the default permissions of the destination.
func copyPermissions(srcFS FileSystem, srcPerm Permissions) Permissions {
	perm := srcPerm & permBits
	if perm == NoPermissions {
		return NoPermissions
	}
	if _, err := fsWritable(srcFS); err != nil {
		perm |= UserWrite
	}
	return perm
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
// A file of dest that is no regular file, like a device, a named pipe or
// a socket, is an error wrapping [os.ErrExist] ([ErrAlreadyExists]), because
// the copy would write into it instead of replacing it.
// dest itself may be a symbolic link to a directory, the copy is written into
// the directory it points to. Below dest a symbolic link is never written
// through, which would write outside of dest: a file or directory of src over
// a symbolic link in dest is an error wrapping [os.ErrExist] ([ErrAlreadyExists])
// that names the link, like for a link that an earlier copy created for a link
// of src that is a directory now, or for a link of dest whose name only
// differs in case from a file or directory of src on a case-insensitive
// destination. The links are checked by path before a file or directory is
// written, so these guarantees about the links of dest assume that dest is
// not changed concurrently by others: a link that another process creates
// between the check and the write is written through.
//
// Two entries of src whose names dest doesn't tell apart, like "A.txt" and
// "a.txt" of a case-sensitive source on a case-insensitive destination, or two
// Unicode normalizations of a name on macOS, would be copied to the same entry,
// the second replacing the first. Every entry of a directory that the copy
// creates is written by the copy, so there an existing entry where an entry of
// src is to be written is an error wrapping [os.ErrExist] ([ErrAlreadyExists])
// that names it. In a directory that existed before, only the local file
// system, also through a [SubFileSystem], can tell an entry that the copy wrote
// from one that was there before, by comparing the files: other file systems
// replace the entry that the copy wrote. Copy into a new directory to have
// every such collision detected.
//
// Copying a directory onto itself is a no-op,
// copying it into one of its sub-directories is an error wrapping [os.ErrInvalid],
// for a local directory also through another path to it, like through
// a symbolic link to a parent directory, a [SubFileSystem] or the local
// layer of an [OverlayFileSystem].
//
// The filter patterns are applied to the names of files and symbolic links,
// sub-directories are always copied, like for [File.ListDirRecursive].
// A symbolic link to a file is followed: the copy is a file with the content
// and permissions of the target. Symbolic links to directories are never
// followed, like by [ListDirRecursiveFileSystem], so a link cycle is not copied
// into itself and a link to the root directory doesn't copy a whole disk.
// Within one file system that implements [SymbolicLinkFileSystem], a link to
// a directory is copied as symbolic link with its target stored verbatim,
// and it replaces a file or link at the same path in dest. Another file
// system can resolve the same target to another directory, even a relative
// target that stays inside src, because it resolves the names of the target
// with its own rules, like following a link in the target before a ".."
// after it, matching names case-insensitively or with another separator.
// So between file systems, which means two instances, also a SubFileSystem
// and its parent, and within one that can't create links,
// CopyRecursive fails before it writes anything with an error wrapping
// [errors.ErrUnsupported] that names the first link to a directory: src is
// checked for such links first, which lists the tree twice. Only a link at src
// is followed, its directory is copied.
// An entry of src with a name that is not valid for dest,
// like the name `..\x` from an archive on a file system with the separator `\`,
// is an error wrapping [os.ErrInvalid], so no file can be written outside of dest.
// For the local file system, also through a [SubFileSystem] or as upper layer
// of an [OverlayFileSystem], the names that [filepath.Localize] rejects are not
// valid: a name with a NUL byte, and on Windows a name with a colon, which
// Windows takes for an alternate data stream of another file, or a reserved
// device name like "NUL" or "COM1", which writes to the device. On Windows
// a name that ends with a dot or a space is not valid either, Windows removes
// them and writes to another entry. The names of the files to copy and of
// their directories are checked while src is listed for the links to
// directories, which happens for every copy to another file system,
// so the copy fails before it writes anything.
// The files are copied with permissions like by [CopyFile],
// the directories with the default permissions of dest.
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
	if srcLocal, destLocal, _, local := fsLocalPaths(srcFS, srcPath, destFS, destPath); local && isLocalInsideDir(srcLocal, destLocal) {
		// Through another path to the directory, like a symbolic link to
		// a parent directory, a bind mount, a SubFileSystem or an overlay
		return fmt.Errorf("%w: can't copy directory %s into itself at %s", os.ErrInvalid, src, dest)
	}
	c := newTreeCopier(ctx, srcFS, destFS, patterns)
	if err := c.checkSource(src, dest); err != nil {
		return err
	}
	return c.copyDir(src, dest, nil)
}

// treeCopier copies directory trees for CopyRecursive and moveByCopy.
type treeCopier struct {
	ctx       context.Context
	patterns  []string               // only files and links with a matching name are copied
	links     SymbolicLinkFileSystem // if not nil, every symbolic link is copied as link within it
	dirLinks  SymbolicLinkFileSystem // the destination if it can create the copies of links to directories
	sameFS    bool                   // the source and the destination are the same file system
	localDest bool                   // the destination writes to the local file system, see writesToLocal
	move      bool                   // the copy of a move, whose destination didn't exist, see destDir
	buf       []byte                 // copy buffer for all files, allocated on first use
}

// newTreeCopier returns a treeCopier from srcFS to destFS
// that copies the files matching patterns.
func newTreeCopier(ctx context.Context, srcFS, destFS FileSystem, patterns []string) *treeCopier {
	return &treeCopier{
		ctx:       ctx,
		patterns:  patterns,
		dirLinks:  fsLinks(destFS),
		sameFS:    srcFS == destFS,
		localDest: writesToLocal(destFS),
	}
}

// validName reports if an entry of the source with name can be written
// to destFS as one entry with that name: see isValidName,
// and for a local destination isValidLocalName.
func (c *treeCopier) validName(destFS FileSystem, name string) bool {
	return isValidName(destFS, name) && (!c.localDest || isValidLocalName(name))
}

// writesToLocal reports if fileSystem writes its files to the local file
// system: it is the LocalFileSystem, a SubFileSystem of one, or an
// OverlayFileSystem whose upper layer, which gets every write, does.
func writesToLocal(fileSystem FileSystem) bool {
	for {
		switch f := fileSystem.(type) {
		case *LocalFileSystem:
			return true
		case *SubFileSystem:
			fileSystem = f.parent
		case *OverlayFileSystem:
			fileSystem = f.upper
		default:
			return false
		}
	}
}

// isValidLocalName reports if the local file system writes a new entry
// with name to the entry of that name in the directory and not to another
// one: filepath.Localize must accept the name, which rejects the names that
// os.DirFS rejects, on every platform a name with a NUL byte, and on Windows
// a name with a colon, which Windows takes for an alternate data stream of
// another file, or a reserved device name like "NUL" or "COM1", which writes
// to the device. On Windows the name must not end with a dot or a space
// either, Windows removes them, so "a." would be written to "a".
// The separators are checked by isValidName.
func isValidLocalName(name string) bool {
	if _, err := filepath.Localize(name); err != nil {
		return false
	}
	return runtime.GOOS != "windows" || !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

// errInvalidName returns the error wrapping os.ErrInvalid for the entry src
// of the source that can't be copied to dest, because its name or the name
// of one of its directories is not valid for dest.
func errInvalidName(src File, name string, dest File) error {
	return fmt.Errorf("%w: can't copy %s to %s, the name %q is not valid there", os.ErrInvalid, errSubject(src), errSubject(dest), name)
}

// canCopyDirLinks reports if links to directories can be copied as links:
// only within one file system, which can create them. Another file system
// can resolve the same target to another directory, see CopyRecursive.
func (c *treeCopier) canCopyDirLinks() bool {
	return c.sameFS && c.dirLinks != nil
}

// errDirLink returns the error wrapping errors.ErrUnsupported
// for the symbolic link to a directory that can't be copied to dest.
func (c *treeCopier) errDirLink(link, dest File) error {
	if !c.sameFS {
		return fmt.Errorf("can't copy the symbolic link to a directory %s to another file system: %w", errSubject(link), errors.ErrUnsupported)
	}
	return fmt.Errorf("can't copy the symbolic link to a directory %s: %w", errSubject(link), NewErrUnsupported(dest.FileSystem(), "CreateSymbolicLink"))
}

// checkSource returns an error for the first entry below the directory src
// that can't be copied to dest, so that the copy fails before it writes
// anything: the error of errInvalidName for a file or link whose name or the
// name of one of its directories below src is not valid for dest, see
// treeCopier.validName, and the error of errDirLink for a symbolic link
// to a directory, which can't be copied to dest.
// The tree is only listed if links to directories can't be copied, which
// includes every copy to another file system. Within one file system that
// copies them the names come from the destination itself, and copyDir
// checks every name again before it writes the entry.
func (c *treeCopier) checkSource(src, dest File) error {
	if c.links != nil || c.canCopyDirLinks() {
		return nil
	}
	srcFS, srcPath := src.ParseRawURI()
	destFS, _ := dest.ParseRawURI()
	srcElems := fsSplitPath(srcFS, srcPath)
	return fsListDirRecursive(c.ctx, srcFS, srcPath, c.patterns, func(info *FileInfo) error {
		if !c.validName(destFS, info.Name) {
			return errInvalidName(info.File, info.Name, dest)
		}
		// The directories are not listed, only their files
		elems := fsSplitPath(srcFS, info.File.Path())
		if len(elems) > len(srcElems) && slices.Equal(elems[:len(srcElems)], srcElems) {
			for _, name := range elems[len(srcElems) : len(elems)-1] {
				if !c.validName(destFS, name) {
					return errInvalidName(info.File, name, dest)
				}
			}
		}
		if !info.IsSymlink {
			return nil
		}
		// A link that can't be followed is an error of copyDir
		target, err := fsStat(srcFS, info.File.Path())
		if err != nil || !target.IsDir {
			return nil
		}
		return c.errDirLink(info.File, dest)
	})
}

// copyDir copies the directory src recursively to dest,
// creating dest if it does not exist and merging with it if it does.
// parent is the directory of dest, nil for the root of the copy.
//
// Only the root of the copy may be a symbolic link to a directory,
// then the copy is written into the directory it points to.
// Below the root an existing symbolic link where a directory or a file is
// to be written is an error of errDestLink, because the copy would be written
// through the link, outside of the destination. Such a link can be the copy
// of a link that is a directory of src now, or a link of the destination
// whose name only differs in case from a directory or file of src on
// a case-insensitive destination. An existing directory below the root
// that the copy wrote for another name of the source is an error of
// errCopiedOver, see destDir.
func (c *treeCopier) copyDir(src, dest File, parent *destDir) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	root := parent == nil
	destFS, destPath := dest.ParseRawURI()
	stat := fsLstat
	if root {
		stat = fsStat
	}
	info, statErr := stat(destFS, destPath)
	switch {
	case statErr == nil && info.IsSymlink && !root:
		return errDestLink(src, dest)
	case statErr == nil && !info.IsDir:
		return fmt.Errorf("can't copy directory %s over a file: %w", errSubject(src), NewErrIsNotDirectory(dest))
	case statErr == nil && !root:
		if err := parent.checkExisting(src, dest); err != nil {
			return err
		}
	case statErr != nil:
		// Also tried if Stat failed for another reason than a missing
		// directory, like on write-only file systems.
		if err := fsMakeAllDirs(destFS, destPath, NoPermissions); err != nil {
			return fmt.Errorf("can't make directory %q: %w", errSubject(dest), err)
		}
	}
	dir := c.newDestDir(dest, statErr)

	srcFS, srcPath := src.ParseRawURI()
	return fsListDir(c.ctx, srcFS, srcPath, nil, func(info *FileInfo) error {
		if !c.validName(destFS, info.Name) {
			return errInvalidName(info.File, info.Name, dest)
		}
		file, fileDest := info.File, dest.Join(info.Name)
		if info.IsSymlink && c.links != nil {
			return dir.recordWrite(fileDest, c.copyLink(file, fileDest))
		}
		if info.IsDir && !info.IsSymlink {
			return dir.recordWrite(fileDest, c.copyDir(file, fileDest, dir))
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, c.patterns)
		if err != nil || !match {
			return err
		}
		perm := info.Permissions
		if info.IsSymlink {
			target, err := fsStat(srcFS, file.Path())
			if err != nil {
				return err
			}
			if target.IsDir {
				return dir.recordWrite(fileDest, c.copyDirLink(file, fileDest, dir))
			}
			// Copy the file that the link points to
			perm = target.Permissions
		}
		if err := checkFileDest(file, fileDest, dir); err != nil {
			return err
		}
		return dir.recordWrite(fileDest, copyFileTo(c.ctx, file, perm, fileDest, &c.buf, nil, false, true))
	})
}

// checkFileDest returns an error if the file src can't be copied to dest
// in the directory dir below the root of a copy: an existing directory is
// an ErrIsDirectory, a symbolic link the error of errDestLink, so the file it
// points to is never written, and a file that is no regular file, like
// a device, a named pipe or a socket, the error of errNotRegular. An existing
// file that the copy wrote for another name of the source is the error of
// errCopiedOver, see destDir. A missing dest or an error of Lstat, like on
// write-only file systems, is left to the writer. The one Lstat replaces
// the check of copyFileTo if dest is a directory.
func checkFileDest(src, dest File, dir *destDir) error {
	destFS, destPath := dest.ParseRawURI()
	info, err := fsLstat(destFS, destPath)
	switch {
	case err != nil:
		return nil
	case info.IsSymlink:
		return errDestLink(src, dest)
	case info.IsDir:
		return NewErrIsDirectory(dest)
	case !info.IsRegular:
		return errNotRegular(src, dest)
	}
	return dir.checkExisting(src, dest)
}

// errNotRegular returns the error wrapping os.ErrExist for the existing file
// dest that is no regular file, like a device, a named pipe or a socket,
// where the file src is to be copied: the copy would write into it,
// a device would even discard the data, instead of replacing it.
func errNotRegular(src, dest File) error {
	return fmt.Errorf("can't copy %s over a file that is no regular file: %w", errSubject(src), NewErrAlreadyExists(dest))
}

// destDir is a directory of the destination of a tree copy. It detects
// an existing entry where an entry of the source is to be written that
// the copy already wrote for another name of the source, which the
// destination doesn't tell apart, like "A.txt" and "a.txt" from
// a case-sensitive source on a case-insensitive destination, or two
// Unicode normalizations of a name on macOS. Writing the second entry would
// replace the first, and a move would then remove the source of the first.
type destDir struct {
	// isNew tells that the copy wrote every existing entry of the directory:
	// the copy created it, or it is a directory of a move, whose destination
	// didn't exist. An entry that is created concurrently counts as well.
	isNew bool

	// written has the entries that the copy wrote into a local directory
	// that existed before, by their size and modification time, which are
	// the same for every name of an entry, so that an existing entry is only
	// compared with os.SameFile to the entries with the same. It is nil for
	// a new directory and for other file systems, which have no identity of
	// a file that tells an entry that the copy wrote from one that was there.
	written map[localEntryKey][]os.FileInfo
}

// localEntryKey is the size and the modification time of a local entry,
// see destDir.written.
type localEntryKey struct {
	size     int64
	modified int64
}

// newLocalEntryKey returns the localEntryKey of info.
func newLocalEntryKey(info os.FileInfo) localEntryKey {
	return localEntryKey{size: info.Size(), modified: info.ModTime().UnixNano()}
}

// newDestDir returns the destDir for the directory dest of the copy, which
// exists now. statErr is the error of the Stat or Lstat of dest before the
// copy wrote it, the directory is new if it didn't exist.
func (c *treeCopier) newDestDir(dest File, statErr error) *destDir {
	if c.move || errors.Is(statErr, os.ErrNotExist) {
		return &destDir{isNew: true}
	}
	if _, _, local := fsLocalPath(dest.ParseRawURI()); local {
		return &destDir{written: make(map[localEntryKey][]os.FileInfo)}
	}
	return &destDir{}
}

// checkExisting returns the error of errCopiedOver if the existing entry
// dest of the directory, where src is to be copied, is one that the copy
// wrote for another name of the source.
func (d *destDir) checkExisting(src, dest File) error {
	if d.isNew || d.wroteEntry(dest) {
		return errCopiedOver(src, dest)
	}
	return nil
}

// wroteEntry reports if the copy wrote the existing local entry dest,
// under any name, see recordWrite.
func (d *destDir) wroteEntry(dest File) bool {
	if len(d.written) == 0 {
		return false
	}
	info, ok := localLstat(dest)
	if !ok {
		return false
	}
	return slices.ContainsFunc(d.written[newLocalEntryKey(info)], func(written os.FileInfo) bool {
		return os.SameFile(written, info)
	})
}

// recordWrite records the entry dest that the copy wrote into a local
// directory that existed before, see destDir.written, if err is nil.
// It returns err, the error of writing dest.
func (d *destDir) recordWrite(dest File, err error) error {
	if err != nil || d.written == nil {
		return err
	}
	if info, ok := localLstat(dest); ok {
		key := newLocalEntryKey(info)
		d.written[key] = append(d.written[key], info)
	}
	return nil
}

// localLstat returns the os.Lstat result of the local path of file, see
// fsLocalPath, and false if file has no local path or os.Lstat fails.
func localLstat(file File) (os.FileInfo, bool) {
	localPath, _, ok := fsLocalPath(file.ParseRawURI())
	if !ok {
		return nil, false
	}
	info, err := os.Lstat(localPath)
	return info, err == nil
}

// errCopiedOver returns the error wrapping os.ErrExist for the existing
// entry dest where the entry src of the source is to be copied, which the
// copy wrote for another name of the source that the destination doesn't
// tell apart from the name of src, see destDir.
func errCopiedOver(src, dest File) error {
	return fmt.Errorf("can't copy %s over an entry that the copy wrote for another name, which the destination doesn't tell apart, like another case: %w", errSubject(src), NewErrAlreadyExists(dest))
}

// errDestLink returns the error wrapping os.ErrExist for the symbolic link
// dest below the root of a copy, where the file or directory src is to be
// copied. Writing through the link would write outside of the destination.
func errDestLink(src, dest File) error {
	return fmt.Errorf("can't copy %s through the symbolic link at the destination: %w", errSubject(src), NewErrAlreadyExists(dest))
}

// copyLink copies the symbolic link src
// as a link with the same target to dest within c.links.
// Only a move sets c.links, see moveByCopy, and like os.Symlink the
// file systems fail to create a link over an existing entry, so the
// link never replaces an entry that the move wrote, see destDir.
func (c *treeCopier) copyLink(src, dest File) error {
	target, err := c.links.ReadSymbolicLink(src.Path())
	if err != nil {
		return err
	}
	return c.links.CreateSymbolicLink(target, dest.Path())
}

// copyDirLink copies the symbolic link src to a directory as a link
// to dest in the directory dir of c.dirLinks for copyDir, see replaceWithLink.
// The link replaces a file or link at dest, which is never written through,
// unless the copy wrote it for another name of the source, see destDir,
// and an existing directory at dest is an ErrIsDirectory.
func (c *treeCopier) copyDirLink(src, dest File, dir *destDir) error {
	if !c.canCopyDirLinks() {
		// checkSource missed the link, because it was created after
		// the check or a native ListDirRecursive followed it
		return c.errDirLink(src, dest)
	}
	destInfo, err := fsLstat(c.dirLinks, dest.Path())
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrWriteOnlyFileSystem):
		// Nothing to replace, or a write-only file system that can't tell
	case err != nil:
		return err
	case destInfo.IsDir && !destInfo.IsSymlink:
		return NewErrIsDirectory(dest)
	default:
		if err := dir.checkExisting(src, dest); err != nil {
			return err
		}
	}
	return c.replaceWithLink(src, dest, destInfo)
}

// replaceWithLink creates dest in c.dirLinks as a symbolic link with the
// verbatim target of the link src of the same file system, which
// canCopyDirLinks must allow. An existing file or link at dest, described
// by destInfo, is replaced like by os.Rename, destInfo is nil if there is none.
// To keep dest if the file system can't create the link, the link is first
// created at a temporary path next to dest and removed again before dest is.
func (c *treeCopier) replaceWithLink(src, dest File, destInfo *FileInfo) error {
	target, err := c.dirLinks.ReadSymbolicLink(src.Path())
	if err != nil {
		return err
	}
	destPath := dest.Path()
	if destInfo == nil {
		return c.dirLinks.CreateSymbolicLink(target, destPath)
	}
	dir, _ := fsSplitDirAndName(c.dirLinks, destPath)
	probePath := c.dirLinks.CleanPath(dir, ".gofs-link-"+fsimpl.RandomString())
	if err := c.dirLinks.CreateSymbolicLink(target, probePath); err != nil {
		return err
	}
	if err := fsRemove(c.dirLinks, probePath); err != nil {
		return err
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	var restoreLink func() error
	if destInfo.IsSymlink {
		restoreLink, err = removeLink(c.dirLinks, destPath)
	} else {
		err = fsRemove(c.dirLinks, destPath)
	}
	if err != nil {
		return err
	}
	err = c.dirLinks.CreateSymbolicLink(target, destPath)
	if err != nil && restoreLink != nil {
		return errors.Join(err, restoreLink())
	}
	return err
}

// removeLink removes the symbolic link at linkPath and returns a function
// that puts the link back in place of what was created at linkPath since,
// for example by a copy that failed. The function is nil if fileSystem
// can't read the target of the link to recreate it.
func removeLink(fileSystem FileSystem, linkPath string) (restore func() error, err error) {
	links, canRestore := fileSystem.(SymbolicLinkFileSystem)
	var target string
	if canRestore {
		target, err = links.ReadSymbolicLink(linkPath)
		if err != nil {
			return nil, err
		}
	}
	if err := fsRemove(fileSystem, linkPath); err != nil {
		return nil, err
	}
	if !canRestore {
		return nil, nil
	}
	return func() error {
		// A failed copy can leave a partial file at the path of the link
		if err := RemoveErrDoesNotExist(fsRemove(fileSystem, linkPath)); err != nil {
			return err
		}
		return links.CreateSymbolicLink(target, linkPath)
	}, nil
}
