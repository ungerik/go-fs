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
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

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
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ListDir lists the directory with one SFTP request and calls
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
		return err
	})
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
		walker := c.sftp.Walk(dirPath)
		for walker.Step() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := walker.Err(); err != nil {
				return f.pathErr(err, walker.Path())
			}
			info := walker.Stat()
			if info.IsDir() {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(info.Name(), patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			noRetry()
			err = callback(fs.NewFileInfo(f.file(walker.Path()), info, isHidden(info.Name())))
			if err != nil {
				return callbackError{err}
			}
		}
		return nil
	})
	if cbErr, ok := errors.AsType[callbackError](err); ok {
		return cbErr.err
	}
	return err
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
	})
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
	})
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
	})
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
	})
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
	})
}

func (f *fileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return f.run(context.Background(), func(c *conn, _ func()) error {
		return f.pathErr(c.sftp.Chmod(filePath, os.FileMode(perm)), filePath)
	})
}

// Move renames filePath to destPath with the overwrite semantics of
// fs.MoveFileSystem, checked before the SFTP RENAME request.
//
// An existing file at destPath is replaced atomically with the
// posix-rename@openssh.com extension if the server supports it.
// Without the extension the file at destPath is removed before the
// rename, so for a moment neither file is at destPath, and if the
// rename fails, the file at destPath is gone.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
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
		// Replace the file or symbolic link at destPath
		noRetry()
		if c.posixRename {
			return f.pathErr(c.sftp.PosixRename(filePath, destPath), filePath)
		}
		err = c.sftp.Remove(destPath)
		if err != nil {
			return f.pathErr(err, destPath)
		}
		return f.pathErr(c.sftp.Rename(filePath, destPath), filePath)
	})
}

// Remove removes a file or an empty directory.
// A non-empty directory is an fs.ErrDirectoryNotEmpty.
func (f *fileSystem) Remove(filePath string) error {
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
	})
}

// RemoveAll removes the file or directory tree over one connection.
// Symbolic links are removed, not followed. A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return f.run(ctx, func(c *conn, _ func()) error {
		info, err := c.sftp.Lstat(filePath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return removeAll(ctx, c.sftp, filePath, info)
	})
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
	})
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
	})
}

func (f *fileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	err = f.run(context.Background(), func(c *conn, _ func()) error {
		targetPath, err = c.sftp.ReadLink(linkPath)
		return f.pathErr(err, linkPath)
	})
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
