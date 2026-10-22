package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
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
// Copying a file onto itself is a no-op.
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
		if srcFS != destFS {
			// The same local file through a SubFileSystem, else the writer
			// would truncate the file that is read. The native CopyFile of
			// LocalFileSystem checks the paths within the file system.
			srcLocal, destLocal, local := fsLocalPaths(srcFS, srcPath, destFS, destPath)
			if local && isLocalSameFile(srcLocal, destLocal, os.Stat) {
				return nil
			}
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
// dest itself may be a symbolic link to a directory, the copy is written into
// the directory it points to. Below dest a symbolic link is never written
// through, which would write outside of dest: a file or directory of src over
// a symbolic link in dest is an error wrapping [os.ErrExist] ([ErrAlreadyExists])
// that names the link, like for a link that an earlier copy created for a link
// of src that is a directory now, or for a link and a directory of src whose
// names only differ in case on a case-insensitive destination.
// Copying a directory onto itself is a no-op,
// copying it into one of its sub-directories is an error wrapping [os.ErrInvalid],
// for a local directory also through another path to it, like through
// a symbolic link to a parent directory or a [SubFileSystem].
//
// The filter patterns are applied to the names of files and symbolic links,
// sub-directories are always copied, like for [File.ListDirRecursive].
// A symbolic link to a file is followed: the copy is a file with the content
// and permissions of the target. Symbolic links to directories are never
// followed, like by [ListDirRecursiveFileSystem], so a link cycle is not copied
// into itself and a link to the root directory doesn't copy a whole disk:
// a link to a directory is copied as symbolic link if dest is on a file system
// that implements [SymbolicLinkFileSystem], and it replaces a file or link
// at the same path in dest. Within one file system its target is stored
// verbatim. Another file system resolves the target to another file, so
// between file systems only a relative target that stays inside src, resolved
// lexically against the directory of the link, is copied, with the separator
// of dest. An absolute target, a target that leaves src, also if it comes
// back like "../src/sub", or a target with a name that dest can't express
// fails the copy like a link that dest can't create: CopyRecursive fails
// before it writes anything with an error wrapping [errors.ErrUnsupported]
// that names the link, so src is checked for such links first, which lists
// the tree twice. Only a link at src is followed.
// An entry of src with a name that is not valid for dest,
// like the name `..\x` from an archive on a file system with the separator `\`,
// is an error wrapping [os.ErrInvalid], so no file can be written outside of dest.
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
	if srcLocal, destLocal, local := fsLocalPaths(srcFS, srcPath, destFS, destPath); local && isLocalInsideDir(srcLocal, destLocal) {
		// Through another path to the directory, like a symbolic link
		// to a parent directory, a bind mount or a SubFileSystem
		return fmt.Errorf("%w: can't copy directory %s into itself at %s", os.ErrInvalid, src, dest)
	}
	c := newTreeCopier(ctx, srcFS, srcPath, destFS, patterns)
	if err := c.checkDirLinks(src, dest); err != nil {
		return err
	}
	return c.copyDir(src, dest, true)
}

// treeCopier copies directory trees for CopyRecursive and moveByCopy.
type treeCopier struct {
	ctx       context.Context
	patterns  []string               // only files and links with a matching name are copied
	links     SymbolicLinkFileSystem // if not nil, every symbolic link is copied as link within it
	dirLinks  SymbolicLinkFileSystem // the destination if it can create the copies of links to directories
	readLinks bool                   // the source can read the targets of its links, see fsReadSymbolicLink
	sameFS    bool                   // the source and the destination are the same file system
	srcRoot   string                 // the path of the copied directory or moved file of the source
	buf       []byte                 // copy buffer for all files, allocated on first use
}

// newTreeCopier returns a treeCopier from srcFS to destFS
// that copies the files matching patterns of the directory
// or the moved file srcRoot.
func newTreeCopier(ctx context.Context, srcFS FileSystem, srcRoot string, destFS FileSystem, patterns []string) *treeCopier {
	return &treeCopier{
		ctx:       ctx,
		patterns:  patterns,
		dirLinks:  fsLinks(destFS),
		readLinks: fsCanReadLinks(srcFS),
		sameFS:    srcFS == destFS,
		srcRoot:   srcRoot,
	}
}

// canCopyDirLinks reports if links to directories can be copied
// as links: the destination can create them and the source can read them.
func (c *treeCopier) canCopyDirLinks() bool {
	return c.dirLinks != nil && c.readLinks
}

// errDirLink returns the error wrapping errors.ErrUnsupported
// for the symbolic link to a directory that can't be copied to dest.
func (c *treeCopier) errDirLink(link, dest File) error {
	err := NewErrUnsupported(dest.FileSystem(), "CreateSymbolicLink")
	if c.dirLinks != nil {
		err = NewErrUnsupported(link.FileSystem(), "ReadSymbolicLink")
	}
	return fmt.Errorf("can't copy the symbolic link to a directory %s: %w", errSubject(link), err)
}

// checkDirLinks returns an error for the first symbolic link to a directory
// below the directory src that copyDir can't copy to dest, so that the copy
// fails before it writes anything: the error of errDirLink if links to
// directories can't be copied, or of dirLinkTarget for a link of another
// file system whose target would point somewhere else at dest.
// The tree is only listed if links to directories can't be copied
// or are copied from another file system.
func (c *treeCopier) checkDirLinks(src, dest File) error {
	if c.links != nil || (c.sameFS && c.canCopyDirLinks()) {
		return nil
	}
	srcFS, srcPath := src.ParseRawURI()
	return fsListDirRecursive(c.ctx, srcFS, srcPath, c.patterns, func(info *FileInfo) error {
		if !info.IsSymlink {
			return nil
		}
		// A link that can't be followed is an error of copyDir
		target, err := fsStat(srcFS, info.File.Path())
		if err != nil || !target.IsDir {
			return nil
		}
		if !c.canCopyDirLinks() {
			return c.errDirLink(info.File, dest)
		}
		_, err = c.dirLinkTarget(info.File)
		return err
	})
}

// copyDir copies the directory src recursively to dest,
// creating dest if it does not exist and merging with it if it does.
//
// Only the root of the copy, passed with root true, may be a symbolic link
// to a directory, then the copy is written into the directory it points to.
// Below the root an existing symbolic link where a directory or a file is
// to be written is an error of errDestLink, because the copy would be written
// through the link, outside of the destination. Such a link can be the copy
// of a link that is a directory of src now, or the copy of a link of src
// that has the name of a directory or file of src on a case-insensitive
// destination.
func (c *treeCopier) copyDir(src, dest File, root bool) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	destFS, destPath := dest.ParseRawURI()
	stat := fsLstat
	if root {
		stat = fsStat
	}
	info, err := stat(destFS, destPath)
	switch {
	case err == nil && info.IsSymlink && !root:
		return errDestLink(src, dest)
	case err == nil && !info.IsDir:
		return fmt.Errorf("can't copy directory %s over a file: %w", errSubject(src), NewErrIsNotDirectory(dest))
	case err != nil:
		// Also tried if Stat failed for another reason than a missing
		// directory, like on write-only file systems.
		if err := fsMakeAllDirs(destFS, destPath, NoPermissions); err != nil {
			return fmt.Errorf("can't make directory %q: %w", errSubject(dest), err)
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
		if info.IsDir && !info.IsSymlink {
			return c.copyDir(file, fileDest, false)
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
				return c.copyDirLink(file, fileDest)
			}
			// Copy the file that the link points to
			perm = target.Permissions
		}
		if err := checkFileDest(file, fileDest); err != nil {
			return err
		}
		return copyFileTo(c.ctx, file, perm, fileDest, &c.buf, nil, false, true)
	})
}

// checkFileDest returns an error if the file src can't be copied to dest
// below the root of a copy: an existing directory is an ErrIsDirectory and
// a symbolic link the error of errDestLink, so the file it points to is never
// written. A missing dest or an error of Lstat, like on write-only file
// systems, is left to the writer. The one Lstat replaces the check of
// copyFileTo if dest is a directory.
func checkFileDest(src, dest File) error {
	destFS, destPath := dest.ParseRawURI()
	info, err := fsLstat(destFS, destPath)
	switch {
	case err != nil:
		return nil
	case info.IsSymlink:
		return errDestLink(src, dest)
	case info.IsDir:
		return NewErrIsDirectory(dest)
	}
	return nil
}

// errDestLink returns the error wrapping os.ErrExist for the symbolic link
// dest below the root of a copy, where the file or directory src is to be
// copied. Writing through the link would write outside of the destination.
func errDestLink(src, dest File) error {
	return fmt.Errorf("can't copy %s through the symbolic link at the destination: %w", errSubject(src), NewErrAlreadyExists(dest))
}

// copyLink copies the symbolic link src
// as a link with the same target to dest within c.links.
func (c *treeCopier) copyLink(src, dest File) error {
	target, err := c.links.ReadSymbolicLink(src.Path())
	if err != nil {
		return err
	}
	return c.links.CreateSymbolicLink(target, dest.Path())
}

// copyDirLink copies the symbolic link src to a directory as a link
// to dest in c.dirLinks for copyDir, see replaceWithLink.
// The link replaces a file or link at dest, which is never written through,
// and an existing directory at dest is an ErrIsDirectory.
func (c *treeCopier) copyDirLink(src, dest File) error {
	if !c.canCopyDirLinks() {
		// checkDirLinks missed the link, because it was created after
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
	}
	return c.replaceWithLink(src, dest, destInfo)
}

// replaceWithLink creates dest in c.dirLinks as a symbolic link with the
// target of the link src, which canCopyDirLinks must allow to read,
// see dirLinkTarget. An existing file or link at dest, described by destInfo,
// is replaced like by os.Rename, destInfo is nil if there is none.
// To keep dest if the file system can't create the link, the link is first
// created at a temporary path next to dest and removed again before dest is.
func (c *treeCopier) replaceWithLink(src, dest File, destInfo *FileInfo) error {
	target, err := c.dirLinkTarget(src)
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

// dirLinkTarget returns the target for the copy of the symbolic link to
// a directory link, which canCopyDirLinks must allow to read.
// Within one file system the target is kept verbatim. Another file system
// resolves the same target to another file, so between file systems only
// a relative target that stays inside the copied directory c.srcRoot is
// copied, written with the separator of the destination, see
// relativeLinkTarget. Every other target is an error wrapping
// errors.ErrUnsupported that names the link.
func (c *treeCopier) dirLinkTarget(link File) (string, error) {
	srcFS, linkPath := link.ParseRawURI()
	target, err := fsReadSymbolicLink(srcFS, linkPath)
	if err != nil {
		return "", err
	}
	if c.sameFS {
		return target, nil
	}
	destTarget, reason := relativeLinkTarget(srcFS, c.srcRoot, linkPath, target, c.dirLinks)
	if reason != "" {
		return "", fmt.Errorf("can't copy the symbolic link to a directory %s to another file system, its target %q %s: %w",
			errSubject(link), target, reason, errors.ErrUnsupported)
	}
	return destTarget, nil
}

// relativeLinkTarget returns target, the target of the symbolic link at
// linkPath of srcFS, for the copy of the link in destFS, or the reason why
// the copy would point to another file than the link: the target must be
// relative and, resolved lexically against the directory of the link, stay
// inside rootPath, the copied directory, or the link itself if it is moved,
// without going above rootPath on the way, because only the copied tree is
// the same in destFS.
//
// The target is split into names with the separator of srcFS and the slash,
// which CleanPath treats as separator too. If destFS has another separator,
// the names are joined with it, so they must be valid names of destFS,
// see isValidName, and the result must not be absolute in destFS.
func relativeLinkTarget(srcFS FileSystem, rootPath, linkPath, target string, destFS FileSystem) (destTarget, reason string) {
	srcSep, destSep := srcFS.Separator(), destFS.Separator()
	switch {
	case target == "":
		return "", "is empty"
	case strings.HasPrefix(target, "/"), strings.HasPrefix(target, srcSep), fsVolumeLen(srcFS, target) > 0:
		return "", "is absolute"
	}
	leaves := "leaves the copied directory"
	if linkPath == rootPath {
		leaves = "points outside of the moved link"
	}
	root := fsSplitPath(srcFS, rootPath)
	linkDir, _ := fsSplitDirAndName(srcFS, linkPath)
	resolved := fsSplitPath(srcFS, linkDir)
	var names []string
	for name := range strings.SplitSeq(strings.ReplaceAll(target, srcSep, "/"), "/") {
		switch name {
		case "":
			continue
		case ".":
		case "..":
			if len(resolved) <= len(root) {
				return "", leaves
			}
			resolved = resolved[:len(resolved)-1]
		default:
			if !isValidName(destFS, name) {
				return "", fmt.Sprintf("has the name %q, which is no name with the separator %q", name, destSep)
			}
			resolved = append(resolved, name)
		}
		names = append(names, name)
	}
	if len(resolved) < len(root) || !slices.Equal(resolved[:len(root)], root) {
		// Only possible for a moved link, whose directory is above rootPath
		return "", leaves
	}
	if srcSep == destSep {
		return target, ""
	}
	destTarget = strings.Join(names, destSep)
	if fsVolumeLen(destFS, destTarget) > 0 {
		return "", "is absolute with the separator " + strconv.Quote(destSep)
	}
	return destTarget, ""
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
