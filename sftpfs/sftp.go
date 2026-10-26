// Package sftpfs implements a file system for SFTP servers.
//
// Dial, DialAndRegister and EnsureRegistered connect to a server with the
// settings of Options. A file system keeps one SSH connection with one
// SFTP session. Keep-alive requests detect a dead connection. After the
// connection is lost, the next operation reconnects with the stored
// settings, calling Options.Credentials again.
//
// An operation that fails because the connection was lost while it ran
// is retried once with a new connection if repeating it can't change the
// result: reading operations, and writes like Truncate, SetPermissions,
// MakeAllDirs and RemoveAll. Operations that change the server in a way
// that can't be repeated, like Remove, Move, MakeDir, CreateSymbolicLink
// and opening a writer, are only retried if the connection was lost before
// their request was sent; else they return the connection error, although
// the server may have executed the request. The listing methods are never
// retried after they called the callback for the first time.
package sftpfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix is the URI scheme prefix of SFTP file systems.
	// The prefix of a file system is Prefix followed by
	// "user@host", with ":port" if the port is not 22.
	Prefix = "sftp://"

	// Separator is the path separator of SFTP file systems.
	Separator = "/"
)

// Compile-time interface checks
var (
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ValidateNameFileSystem     = new(fileSystem)
	_ fs.AppendWriterFileSystem     = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.TruncateFileSystem         = new(fileSystem)
	_ fs.TouchFileSystem            = new(fileSystem)
	_ fs.MakeAllDirsFileSystem      = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.MoveFileSystem             = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
	_ fs.PermissionsFileSystem      = new(fileSystem)
	_ fs.SymbolicLinkFileSystem     = new(fileSystem)
)

type fileSystem struct {
	fsimpl.PathHelper

	target *target
	opts   Options

	// life is canceled by Close and ends a reconnect in progress
	life   context.Context
	cancel context.CancelFunc

	mtx    sync.Mutex
	conn   *conn     // nil before the first dial and after a lost connection
	redial *dialCall // the reconnect in progress or nil
	closed bool
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system.
func (f *fileSystem) ID() string {
	return f.URIPrefix
}

func (f *fileSystem) Name() string {
	return "SFTP"
}

// String returns the name and the URI prefix of the file system,
// which never contains a password.
func (f *fileSystem) String() string {
	return f.Name() + " " + f.URIPrefix
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

// file returns the File of a clean path of the file system.
func (f *fileSystem) file(filePath string) fs.File {
	return f.JoinCleanFile(filePath)
}

// checkPath returns an error wrapping os.ErrInvalid for a path with
// a NUL character, at which the server would end the path and use
// another file, and for a path with a backslash unless
// Options.AllowBackslashInNames is set.
// run checks the paths of an operation with it.
func (f *fileSystem) checkPath(paths ...string) error {
	for _, filePath := range paths {
		if strings.Contains(filePath, "\x00") {
			return fmt.Errorf("%w: sftpfs path %.200q contains a NUL character", os.ErrInvalid, filePath)
		}
		if f.opts.AllowBackslashInNames {
			continue
		}
		err := fsimpl.CheckNoBackslash("sftpfs", filePath, "which SFTP servers on Windows take for a separator, see Options.AllowBackslashInNames")
		if err != nil {
			return err
		}
	}
	return nil
}

// ValidateName implements fs.ValidateNameFileSystem: a name that
// checkPath rejects and a name that is not valid UTF-8, which go-fs
// doesn't write and servers on file systems like APFS refuse, are errors
// wrapping os.ErrInvalid, so a copy or move into the file system fails
// before it writes anything instead of at that name. Existing entries are
// read with every name, and copied and moved with it within the server.
func (f *fileSystem) ValidateName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: sftpfs name %.200q is not valid UTF-8", os.ErrInvalid, name)
	}
	return f.checkPath(name)
}

// pathErr returns err with the os.ErrNotExist and os.ErrPermission
// errors of pkg/sftp mapped to the typed errors of package fs for filePath.
func (f *fileSystem) pathErr(err error, filePath string) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fs.NewErrDoesNotExist(f.file(filePath))
	case errors.Is(err, os.ErrPermission):
		return fs.NewErrPermission(f.file(filePath))
	}
	return err
}

// isHidden reports whether a name or path is hidden by the dot rule.
func isHidden(filePath string) bool {
	return strings.HasPrefix(path.Base(filePath), ".")
}

// stat returns the FileInfo with the symlink flag set
// from Lstat and the other fields from Stat like os does.
func (f *fileSystem) stat(client *sftp.Client, filePath string) (*fs.FileInfo, error) {
	linkInfo, err := client.Lstat(filePath)
	if err != nil {
		return nil, f.pathErr(err, filePath)
	}
	info := linkInfo
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		info, err = client.Stat(filePath)
		if err != nil {
			return nil, f.pathErr(err, filePath)
		}
	}
	fileInfo := fs.NewFileInfo(f.file(filePath), info, isHidden(filePath))
	fileInfo.IsSymlink = linkInfo.Mode()&os.ModeSymlink != 0
	return fileInfo, nil
}

func (f *fileSystem) Stat(filePath string) (info *fs.FileInfo, err error) {
	err = f.run(context.Background(), func(c *conn, _ func()) error {
		info, err = f.stat(c.sftp, filePath)
		return err
	}, filePath)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ListDir lists the directory with one ReadDir call and calls
// callback after the listing, so that a lost connection never
// repeats a callback.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	var infos []os.FileInfo
	err := f.run(ctx, func(c *conn, _ func()) (err error) {
		infos, err = c.sftp.ReadDirContext(ctx, dirPath)
		if err != nil && ctx.Err() == nil && !isConnectionError(err) {
			// Distinguish a missing directory from a path that is not a directory
			info, statErr := c.sftp.Stat(dirPath)
			switch {
			case statErr == nil && !info.IsDir():
				return fs.NewErrIsNotDirectory(f.file(dirPath))
			case errors.Is(err, os.ErrNotExist) || errors.Is(statErr, os.ErrNotExist):
				return fs.NewErrDoesNotExist(f.file(dirPath))
			}
		}
		return f.pathErr(err, dirPath)
	}, dirPath)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		match, err := fsimpl.MatchAnyPattern(info.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		err = callback(fs.NewFileInfo(f.JoinCleanFile(dirPath, info.Name()), info, isHidden(info.Name())))
		if err != nil {
			return err
		}
	}
	return nil
}

// ListDirRecursive walks the directory tree with one connection
// and calls callback for every file (not directory) matching the patterns.
// A symbolic link at dirPath is followed, the files of the directory
// it points to are listed below dirPath. Symbolic links in the tree are
// listed like files and not followed, the FileInfo of a link to a directory
// has IsDir false, see fs.ListDirRecursiveFileSystem.
// A lost connection is only retried before the first callback.
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	err := f.run(ctx, func(c *conn, noRetry func()) error {
		info, err := c.sftp.Stat(dirPath)
		if err != nil {
			return f.pathErr(err, dirPath)
		}
		if !info.IsDir() {
			return fs.NewErrIsNotDirectory(f.file(dirPath))
		}
		return f.walk(ctx, c.sftp, dirPath, patterns, noRetry, callback)
	}, dirPath)
	if cbErr, ok := errors.AsType[callbackError](err); ok {
		return cbErr.err
	}
	return err
}

// walk calls callback for the files below the directory dirPath matching
// the patterns, depth first in the order of the listings. Unlike the walk
// of pkg/sftp, which takes the Lstat of its root, walk lists the directory
// that a symbolic link at dirPath points to. noRetry is called before
// every callback, the errors of callback are returned as callbackError.
func (f *fileSystem) walk(ctx context.Context, client *sftp.Client, dirPath string, patterns []string, noRetry func(), callback func(*fs.FileInfo) error) error {
	entries, err := client.ReadDirContext(ctx, dirPath)
	if err != nil {
		return f.pathErr(err, dirPath)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := path.Join(dirPath, entry.Name())
		if entry.IsDir() {
			err = f.walk(ctx, client, entryPath, patterns, noRetry, callback)
			if err != nil {
				return err
			}
			continue
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		noRetry()
		err = callback(fs.NewFileInfo(f.file(entryPath), entry, isHidden(entry.Name())))
		if err != nil {
			return callbackError{err}
		}
	}
	return nil
}

// sftpFile is a file opened by openFile.
//
// OpenSSH opens a directory for reading and only fails reading it,
// with a generic failure that sftpFile maps to fs.ErrIsDirectory.
type sftpFile struct {
	*sftp.File
	fs   *fileSystem
	path string
}

func (f *sftpFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	return n, f.readErr(err)
}

func (f *sftpFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(p, off)
	return n, f.readErr(err)
}

func (f *sftpFile) WriteTo(w io.Writer) (int64, error) {
	n, err := f.File.WriteTo(w)
	return n, f.readErr(err)
}

// readErr returns an fs.ErrIsDirectory for a generic failure
// reading a directory, other errors are returned unchanged.
func (f *sftpFile) readErr(err error) error {
	if status, ok := errors.AsType[*sftp.StatusError](err); ok && status.FxCode() == sftp.ErrSSHFxFailure {
		if info, statErr := f.File.Stat(); statErr == nil && info.IsDir() {
			return fs.NewErrIsDirectory(f.fs.file(f.path))
		}
	}
	return err
}

// openFile opens a file with the flags. A file created by the
// call gets the permissions perm if perm is not zero.
func (f *fileSystem) openFile(filePath string, flags int, perm fs.Permissions) (*sftpFile, error) {
	var opened *sftpFile
	err := f.run(context.Background(), func(c *conn, noRetry func()) error {
		created := false
		if perm != 0 && flags&os.O_CREATE != 0 {
			_, statErr := c.sftp.Stat(filePath)
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				created = true
			case isConnectionError(statErr):
				return statErr
			}
		}
		if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
			// Truncating, creating or changing the permissions twice
			// can give another result than doing it once
			noRetry()
		}
		file, err := c.sftp.OpenFile(filePath, flags)
		if err != nil {
			return f.openErr(c.sftp, filePath, err)
		}
		if created {
			err = file.Chmod(os.FileMode(perm))
			if err != nil {
				return errors.Join(err, file.Close())
			}
		}
		if flags&os.O_APPEND != 0 {
			// The client sends the offset of every write, and not every
			// server ignores it for files opened for appending:
			// OpenSSH on Linux does, the server of pkg/sftp doesn't
			_, err = file.Seek(0, io.SeekEnd)
			if err != nil {
				return errors.Join(err, file.Close())
			}
		}
		opened = &sftpFile{File: file, fs: f, path: filePath}
		return nil
	}, filePath)
	if err != nil {
		return nil, err
	}
	return opened, nil
}

// openErr maps the error of opening filePath: a generic failure
// for a directory to fs.ErrIsDirectory and the os errors to the
// typed errors of package fs.
func (f *fileSystem) openErr(client *sftp.Client, filePath string, err error) error {
	if isConnectionError(err) {
		return err
	}
	if status, ok := errors.AsType[*sftp.StatusError](err); ok && status.FxCode() == sftp.ErrSSHFxFailure {
		if info, statErr := client.Stat(filePath); statErr == nil && info.IsDir() {
			return fs.NewErrIsDirectory(f.file(filePath))
		}
	}
	return f.pathErr(err, filePath)
}

func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	file, err := f.openFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	file, err := f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *fileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	file, err := f.openFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	file, err := f.openFile(filePath, os.O_RDWR|os.O_CREATE, perm)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *fileSystem) Truncate(ctx context.Context, filePath string, size int64) error {
	return f.run(ctx, func(c *conn, _ func()) error {
		return f.pathErr(c.sftp.Truncate(filePath, size), filePath)
	}, filePath)
}

// Touch updates the modification time of an existing file in place
// via the SFTP SETSTAT packet and only creates an empty file
// with the permissions perm when it does not exist yet.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	return f.run(context.Background(), func(c *conn, noRetry func()) error {
		_, err := c.sftp.Stat(filePath)
		if errors.Is(err, os.ErrNotExist) {
			noRetry()
			file, err := c.sftp.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
			if err != nil {
				return f.pathErr(err, filePath)
			}
			if perm != 0 {
				err = file.Chmod(os.FileMode(perm))
			}
			return errors.Join(err, file.Close())
		}
		if err != nil {
			return f.pathErr(err, filePath)
		}
		now := time.Now()
		return f.pathErr(c.sftp.Chtimes(filePath, now, now), filePath)
	}, filePath)
}

func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return f.run(context.Background(), func(c *conn, noRetry func()) error {
		noRetry()
		err := c.sftp.Mkdir(dirPath)
		if err != nil {
			if isConnectionError(err) {
				return err
			}
			// SFTP reports a generic failure for an existing path,
			// map it to os.ErrExist like os.Mkdir does
			if _, statErr := c.sftp.Lstat(dirPath); statErr == nil {
				return fs.NewErrAlreadyExists(f.file(dirPath))
			}
			return f.pathErr(err, dirPath)
		}
		if perm != 0 {
			return c.sftp.Chmod(dirPath, os.FileMode(perm))
		}
		return nil
	}, dirPath)
}

// MakeAllDirs creates the directory and all missing parents
// with a single SFTP client MkdirAll call.
func (f *fileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	return f.run(context.Background(), func(c *conn, _ func()) error {
		info, err := c.sftp.Stat(dirPath)
		if err == nil {
			if !info.IsDir() {
				return fs.NewErrIsNotDirectory(f.file(dirPath))
			}
			return nil
		}
		err = c.sftp.MkdirAll(dirPath)
		if err != nil {
			return f.pathErr(err, dirPath)
		}
		if perm != 0 {
			return c.sftp.Chmod(dirPath, os.FileMode(perm))
		}
		return nil
	}, dirPath)
}

func (f *fileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return f.run(context.Background(), func(c *conn, _ func()) error {
		return f.pathErr(c.sftp.Chmod(filePath, os.FileMode(perm)), filePath)
	}, filePath)
}

// Move renames filePath to destPath with the overwrite semantics of
// fs.MoveFileSystem, checked before the SFTP RENAME request.
//
// An existing file at destPath is replaced atomically with the
// posix-rename@openssh.com extension if the server supports it.
// Without the extension the file at destPath is removed before the
// rename, so for a moment neither file is at destPath, and if the
// rename fails, the file at destPath is gone. If destPath may be the
// file at filePath under another name, see maybeSameFile, that file is
// renamed to a temporary name in the directory of destPath first, and
// destPath is only removed if it still exists then.
//
// A symbolic link at filePath is moved as link, not what it points to.
// Moving it onto the file it points to is an error wrapping os.ErrInvalid
// like the "are the same file" of mv, because the rename would replace the
// file with a link to itself. SFTP has no inode numbers and no link counts,
// so a destination with the size, mode, owner and modification time of the
// file that the link points to is taken for that file, see isLinkOntoTarget.
// Unlike LocalFileSystem.Move, a move onto another hard link of the file
// is refused too.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	// Checked here, because the checks before f.run already answer
	if err := f.checkPath(filePath, destPath); err != nil {
		return err
	}
	// The checks before f.run need no request, but must not answer after Close
	if err := f.checkOpen(ctx); err != nil {
		return err
	}
	filePath = path.Clean(filePath)
	destPath = path.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	if strings.HasPrefix(destPath, strings.TrimSuffix(filePath, Separator)+Separator) {
		return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, f.file(filePath), f.file(destPath))
	}
	return f.run(ctx, func(c *conn, noRetry func()) error {
		srcInfo, err := c.sftp.Lstat(filePath)
		if err != nil {
			return f.pathErr(err, filePath)
		}
		destInfo, err := c.sftp.Lstat(destPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			noRetry()
			return f.pathErr(c.sftp.Rename(filePath, destPath), filePath)
		case err != nil:
			return f.pathErr(err, destPath)
		case destInfo.IsDir():
			return fs.NewErrAlreadyExists(f.file(destPath))
		case srcInfo.IsDir():
			return fs.NewErrIsNotDirectory(f.file(destPath))
		}
		if srcInfo.Mode()&os.ModeSymlink != 0 {
			onTarget, err := isLinkOntoTarget(c.sftp, filePath, destInfo)
			if err != nil {
				return err
			}
			if onTarget {
				return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, f.file(filePath), f.file(destPath))
			}
		}
		// Replace the file or symbolic link at destPath
		noRetry()
		if c.posixRename {
			return f.pathErr(c.sftp.PosixRename(filePath, destPath), filePath)
		}
		if maybeSameFile(filePath, destPath, srcInfo, destInfo) {
			return f.moveViaTemp(c.sftp, filePath, destPath)
		}
		err = c.sftp.Remove(destPath)
		if err != nil {
			return f.pathErr(err, destPath)
		}
		return f.pathErr(c.sftp.Rename(filePath, destPath), filePath)
	})
}

// maybeSameFile reports whether filePath and destPath with the Lstat infos
// srcInfo and destInfo may be the same file under two names: names that
// only differ in case on a case-insensitive server, or a path through a
// symbolic link to the directory of the other path, see maybeSameAttrs.
func maybeSameFile(filePath, destPath string, srcInfo, destInfo os.FileInfo) bool {
	return strings.EqualFold(filePath, destPath) || maybeSameAttrs(srcInfo, destInfo)
}

// maybeSameAttrs reports whether the infos a and b may describe the same
// file. SFTP has no inode numbers to tell, so equal attributes are taken
// for the same file.
func maybeSameAttrs(a, b os.FileInfo) bool {
	if a.Mode() != b.Mode() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	aStat, aOK := a.Sys().(*sftp.FileStat)
	bStat, bOK := b.Sys().(*sftp.FileStat)
	return !aOK || !bOK || (aStat.UID == bStat.UID && aStat.GID == bStat.GID)
}

// isLinkOntoTarget reports whether the symbolic link at linkPath may point
// to the file that destInfo from Lstat describes, the destination of a move
// of the link. Renaming the link onto that file would remove the file and
// leave a link that points to itself. The Stat info of the link, which
// follows every link on the way, is compared with destInfo by maybeSameAttrs.
// SFTP reports no link count either, which could tell that other hard links
// keep the file, so equal attributes always refuse the move, also those of
// another file with the same size, mode, owner and modification second.
// A link whose target can't be read, like a dangling one, points to no
// existing file. The error is that of a lost connection only, so that
// fileSystem.run retries the move.
func isLinkOntoTarget(client *sftp.Client, linkPath string, destInfo os.FileInfo) (bool, error) {
	targetInfo, err := client.Stat(linkPath)
	switch {
	case isConnectionError(err):
		return false, err
	case err != nil:
		return false, nil
	}
	return maybeSameAttrs(targetInfo, destInfo), nil
}

// moveViaTemp moves filePath to an existing destPath without the
// posix-rename extension if destPath may be filePath under another name.
// Removing destPath first would remove filePath then, so filePath is
// renamed to a temporary name in the directory of destPath first. If
// destPath still exists after that, it is another file and removed.
// If a later step fails, the file is renamed back to filePath.
func (f *fileSystem) moveViaTemp(client *sftp.Client, filePath, destPath string) error {
	tempPath := path.Join(path.Dir(destPath), ".sftpfs-move-"+rand.Text())
	err := client.Rename(filePath, tempPath)
	if err != nil {
		return f.pathErr(err, filePath)
	}
	_, err = client.Lstat(destPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// destPath was filePath under another name
		err = client.Rename(tempPath, destPath)
	case err == nil:
		err = client.Remove(destPath)
		if err == nil {
			err = client.Rename(tempPath, destPath)
		}
	}
	if err == nil {
		return nil
	}
	if backErr := client.Rename(tempPath, filePath); backErr != nil {
		return fmt.Errorf("%w, and renaming %s back failed, it is at %s: %w", f.pathErr(err, destPath), f.file(filePath), f.file(tempPath), backErr)
	}
	return f.pathErr(err, destPath)
}

// Remove removes a file or an empty directory.
// A non-empty directory is an fs.ErrDirectoryNotEmpty.
// The root directory is never removed, that is an error wrapping os.ErrInvalid,
// also for a relative path like "", "." or "dir/..", which the server
// resolves against the login directory.
func (f *fileSystem) Remove(filePath string) error {
	if isRoot(filePath) {
		return f.removeRootErr(context.Background())
	}
	return f.run(context.Background(), func(c *conn, noRetry func()) error {
		info, err := c.sftp.Lstat(filePath)
		if err != nil {
			return f.pathErr(err, filePath)
		}
		noRetry()
		if !info.IsDir() {
			return f.pathErr(c.sftp.Remove(filePath), filePath)
		}
		err = c.sftp.RemoveDirectory(filePath)
		if err == nil || isConnectionError(err) {
			return err
		}
		// OpenSSH reports a non-empty directory as generic failure
		if entries, readErr := c.sftp.ReadDir(filePath); readErr == nil && len(entries) > 0 {
			return fs.NewErrDirectoryNotEmpty(f.file(filePath))
		}
		return f.pathErr(err, filePath)
	}, filePath)
}

// RemoveAll removes the file or directory tree over one connection.
// Symbolic links are removed, not followed. A missing path is not an error.
// The root directory is never removed, that is an error wrapping os.ErrInvalid,
// also for a relative path like "", "." or "dir/..", see Remove.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if isRoot(filePath) {
		return f.removeRootErr(ctx)
	}
	return f.run(ctx, func(c *conn, _ func()) error {
		info, err := c.sftp.Lstat(filePath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return f.pathErr(err, filePath)
		}
		return removeAll(ctx, c.sftp, filePath, info)
	}, filePath)
}

// isRoot reports whether filePath, cleaned as an absolute path, is the root
// directory. A relative path that cleans to "." is the login directory
// for the server and the root directory of the file system.
func isRoot(filePath string) bool {
	return path.Clean(Separator+filePath) == Separator
}

// removeRootErr returns the error of Remove and RemoveAll for the root
// directory, which is never removed: fs.ErrFileSystemClosed after Close
// and the error of a done ctx like for every other path, else an error
// wrapping os.ErrInvalid. No request is sent to the server.
func (f *fileSystem) removeRootErr(ctx context.Context) error {
	if err := f.checkOpen(ctx); err != nil {
		return err
	}
	return fmt.Errorf("%w: can't remove the root directory of %s", os.ErrInvalid, f)
}

// removeAll removes path with the Lstat info recursively.
func removeAll(ctx context.Context, client *sftp.Client, path string, info os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !info.IsDir() {
		return fs.RemoveErrDoesNotExist(client.Remove(path))
	}
	entries, err := client.ReadDir(path)
	if err != nil {
		return fs.RemoveErrDoesNotExist(err)
	}
	for _, entry := range entries {
		err = removeAll(ctx, client, path+Separator+entry.Name(), entry)
		if err != nil {
			return err
		}
	}
	return fs.RemoveErrDoesNotExist(client.RemoveDirectory(path))
}

// Lstat returns the FileInfo of the file without following a symbolic link.
func (f *fileSystem) Lstat(filePath string) (fileInfo *fs.FileInfo, err error) {
	err = f.run(context.Background(), func(c *conn, _ func()) error {
		info, err := c.sftp.Lstat(filePath)
		if err != nil {
			return f.pathErr(err, filePath)
		}
		fileInfo = fs.NewFileInfo(f.file(filePath), info, isHidden(filePath))
		return nil
	}, filePath)
	if err != nil {
		return nil, err
	}
	return fileInfo, nil
}

func (f *fileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	return f.run(context.Background(), func(c *conn, noRetry func()) error {
		noRetry()
		err := c.sftp.Symlink(targetPath, linkPath)
		if err != nil && !isConnectionError(err) {
			if _, statErr := c.sftp.Lstat(linkPath); statErr == nil {
				return fs.NewErrAlreadyExists(f.file(linkPath))
			}
			return f.pathErr(err, linkPath)
		}
		return err
	}, targetPath, linkPath)
}

func (f *fileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	err = f.run(context.Background(), func(c *conn, _ func()) error {
		targetPath, err = c.sftp.ReadLink(linkPath)
		return f.pathErr(err, linkPath)
	}, linkPath)
	if err != nil {
		return "", err
	}
	return targetPath, nil
}

// Close closes the SSH connection, ends a reconnect in progress and
// unregisters the file system. Afterwards every method returns
// fs.ErrFileSystemClosed. Calling Close more than once is a no-op.
func (f *fileSystem) Close() error {
	f.mtx.Lock()
	if f.closed {
		f.mtx.Unlock()
		return nil
	}
	f.closed = true
	c := f.conn
	f.conn = nil
	f.mtx.Unlock()

	if f.cancel != nil {
		f.cancel()
	}
	fs.Unregister(f)
	if c == nil {
		return nil
	}
	return c.close()
}
