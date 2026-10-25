// Package smbfs implements an SMB2/3 client file system for Windows
// shares, Samba and NAS devices using the pure Go github.com/cloudsoda/go-smb2,
// the maintained fork of github.com/hirochachacha/go-smb2.
//
// SMB shares have real directories and files with random access, so the
// file system implements nearly every optional interface natively:
// append and read-write handles, Truncate, Touch, MakeAllDirs,
// RemoveAll, Move (server-side rename), SetPermissions and symbolic links.
//
// A file system keeps one SMB session that operations use concurrently.
// When its connection broke, for example because a Windows server
// disconnected the idle session, the next operation dials a new session
// before it sends its request. An operation that fails because the
// connection broke while a request was on the way is retried once on a
// new session, unless the request changes the share: if the connection
// broke after Remove, MakeDir or CreateSymbolicLink sent their request,
// or Move its rename after the checks, they return the connection error,
// because the server may have executed it. Open file handles belong to
// their session and fail after a reconnect.
//
// Paths have the separator "/". A path with a backslash is an error
// wrapping os.ErrInvalid, because SMB servers take it for a separator.
//
// URIs have the form "smb://user@host:port/share/path" and never contain a
// password; the port is omitted if it is 445, and "smb://user@host:445/share"
// is an alias. Without a user and password the session is anonymous and
// the URIs have the form "smb://host/share/path".
package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudsoda/go-smb2"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix is the URI scheme prefix of smbfs URIs: smb://user@host/share
	Prefix = "smb://"

	// Separator used in smbfs paths. Paths must not contain backslashes,
	// the separator of SMB servers.
	Separator = "/"
)

// Compile-time interface checks
var (
	_ fs.FileSystem             = new(fileSystem)
	_ fs.WriteFileSystem        = new(fileSystem)
	_ fs.ReadAllFileSystem      = new(fileSystem)
	_ fs.WriteAllFileSystem     = new(fileSystem)
	_ fs.AppendWriterFileSystem = new(fileSystem)
	_ fs.ReadWriterFileSystem   = new(fileSystem)
	_ fs.TruncateFileSystem     = new(fileSystem)
	_ fs.TouchFileSystem        = new(fileSystem)
	_ fs.MakeAllDirsFileSystem  = new(fileSystem)
	_ fs.RemoveAllFileSystem    = new(fileSystem)
	_ fs.MoveFileSystem         = new(fileSystem)
	_ fs.PermissionsFileSystem  = new(fileSystem)
	_ fs.SymbolicLinkFileSystem = new(fileSystem)
)

type fileSystem struct {
	fsimpl.PathHelper
	cfg *dialConfig

	closed  atomic.Bool
	closing context.Context // canceled by Close to abort running operations
	cancel  context.CancelCauseFunc

	mtx     sync.Mutex
	sess    *session  // guarded by mtx
	dialing *dialCall // reconnect in progress, guarded by mtx
}

// Dial connects to an SMB share and returns its file system without
// registering it. The address has the form [smb://][user[:password]@]host[:port]/share;
// the default port is 445. See Options for the credentials.
// A '/', '?' or '#' of the username or password must be percent-encoded,
// like "%2F" for '/', and so must an '@' of the share ("%40"): an address
// with an '@' after the host is rejected, because that '@' can be the end
// of a password with an unencoded '/', '?' or '#'.
// The URI prefix is "smb://user@host/share", or "smb://host/share"
// for an anonymous session.
//
// The context limits the dial including the authentication and
// mounting the share, see also Options.Timeout.
func Dial(ctx context.Context, address string, opts *Options) (fs.FileSystem, error) {
	cfg, err := parseAddress(address, opts)
	if err != nil {
		return nil, err
	}
	return dialFileSystem(ctx, cfg)
}

// dialFileSystem dials the first session of a new file system.
func dialFileSystem(ctx context.Context, cfg *dialConfig) (fs.FileSystem, error) {
	s, err := dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: cfg.prefix, AltPrefixes: cfg.aliases, Rooted: true},
		cfg:        cfg,
		sess:       s,
	}
	f.closing, f.cancel = context.WithCancelCause(context.Background())
	return f, nil
}

// DialAndRegister connects to an SMB share like Dial and registers its
// file system, so that fs.File URIs with its prefix use it.
// It returns an error wrapping fs.ErrAlreadyRegistered if another
// file system is registered with the same prefix.
// Close the file system with fs.GetFileSystemByPrefixOrNil(prefix).Close()
// or use EnsureRegistered, which returns a free function.
func DialAndRegister(ctx context.Context, address string, opts *Options) error {
	f, err := Dial(ctx, address, opts)
	if err != nil {
		return err
	}
	if err := fs.Register(f); err != nil {
		return errors.Join(err, f.Close())
	}
	return nil
}

// EnsureRegistered makes sure that a file system for the address is
// registered, see Dial for the address. The first call dials and registers
// it, later calls for the same URI prefix share it; concurrent calls dial
// only once. Every call increases a reference count. The returned free
// function decreases it and closes and unregisters the file system after
// the last reference if EnsureRegistered dialed it. A file system that was
// registered otherwise, for example by DialAndRegister, is never closed
// by free. free is never nil and can be called more than once.
// The options are only used if the file system is dialed.
func EnsureRegistered(ctx context.Context, address string, opts *Options) (free func() error, err error) {
	cfg, err := parseAddress(address, opts)
	if err != nil {
		return nop, err
	}
	_, free, err = fs.EnsureRegistered(ctx, cfg.prefix, func(ctx context.Context) (fs.FileSystem, error) {
		return dialFileSystem(ctx, cfg)
	})
	if err != nil {
		return nop, err
	}
	return free, nil
}

func nop() error { return nil }

// opContext returns a context for an operation that is canceled with
// the cause fs.ErrFileSystemClosed when the file system is closed.
func (f *fileSystem) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(f.closing, func() { cancel(fs.ErrFileSystemClosed) })
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the URI prefix without the scheme: user@host/share.
func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

func (f *fileSystem) Name() string {
	return "SMB"
}

// String returns the name and the URI prefix, which never contains a password.
func (f *fileSystem) String() string {
	return f.Name() + " " + f.URIPrefix
}

func (f *fileSystem) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}

// checkPath returns fs.ErrEmptyPath for an empty path and an error
// wrapping os.ErrInvalid for a path with a backslash.
//
// SMB servers separate the names of a path with backslashes and go-smb2
// converts the separator "/" to them. A name like `..\x` is a single name
// for the separator "/", also for SubFileSystem and CopyRecursive, but
// the server would split it and resolve "..", so the name would reach
// a file outside of the directory that it is meant to be in.
func checkPath(filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	return fsimpl.CheckNoBackslash("smbfs", filePath, "which SMB servers take for a separator")
}

// checkLinkTarget checks the target of a symbolic link with checkPath and
// returns an error wrapping os.ErrInvalid for a relative target that
// go-smb2 doesn't store as a relative path.
//
// Share.Symlink of go-smb2 converts "/" to `\` and strips leading `.\`.
// Then it stores a target with ':' as second character, like "C:x" or
// "C:/x", as the absolute Windows path `\??\C:x`, a target with a leading
// `\`, like ".//x", as absolute path, and panics for an empty target,
// like ".". For the file system these are relative targets, which
// SubFileSystem for example stores verbatim, so the link would point out
// of the directory of the view.
func checkLinkTarget(targetPath string) error {
	if err := checkPath(targetPath); err != nil {
		return err
	}
	if strings.HasPrefix(targetPath, Separator) {
		return nil // An absolute target is absolute for go-smb2 too
	}
	stripped := targetPath
	for strings.HasPrefix(stripped, "./") {
		stripped = stripped[2:]
	}
	switch {
	case stripped == "" || stripped == ".":
		return fmt.Errorf("%w: smbfs can't store the symbolic link target %q", os.ErrInvalid, targetPath)
	case strings.HasPrefix(stripped, Separator) || (len(stripped) >= 2 && stripped[1] == ':'):
		return fmt.Errorf("%w: smbfs would store the relative symbolic link target %q as an absolute path", os.ErrInvalid, targetPath)
	}
	return nil
}

// sharePath checks a file system path with checkPath and returns
// its share relative path, the empty string for the root.
// Every path is translated with it before it is sent to the server.
func sharePath(filePath string) (string, error) {
	if err := checkPath(filePath); err != nil {
		return "", err
	}
	return strings.Trim(path.Clean("/"+filePath), Separator), nil
}

// wrapErr maps the errors of go-smb2 to the fs error types for filePath.
func (f *fileSystem) wrapErr(filePath string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return fs.NewErrDoesNotExist(f.file(filePath))
	case errors.Is(err, os.ErrExist):
		return fs.NewErrAlreadyExists(f.file(filePath))
	case errors.Is(err, os.ErrPermission):
		return fs.NewErrPermission(f.file(filePath))
	case ntStatus(err) == statusDirectoryNotEmpty:
		return fs.NewErrDirectoryNotEmpty(f.file(filePath))
	case ntStatus(err) == statusNotADirectory:
		return fs.NewErrIsNotDirectory(f.file(filePath))
	case ntStatus(err) == statusFileIsADirectory:
		return fs.NewErrIsDirectory(f.file(filePath))
	}
	return err
}

// dirErr maps the error of reading or writing filePath, with the share
// path p, to fs.ErrIsDirectory if it is a directory. Servers report that
// in different ways, Samba for example with STATUS_INVALID_PARAMETER for
// opening a directory for writing and STATUS_INVALID_DEVICE_REQUEST for
// reading it.
func (f *fileSystem) dirErr(share *smb2.Share, filePath, p string, err error) error {
	err = f.wrapErr(filePath, err)
	if err == nil || isConnectionError(err) || errors.Is(err, os.ErrNotExist) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if info, statErr := share.Stat(p); statErr == nil && info.IsDir() {
		return fs.NewErrIsDirectory(f.file(filePath))
	}
	return err
}

///////////////////////////////////////////////////////////////////////////////
// Reading

func (f *fileSystem) rootInfo() *fs.FileInfo {
	return &fs.FileInfo{
		File:        f.RootDir(),
		Name:        Separator,
		Exists:      true,
		IsDir:       true,
		Permissions: fs.AllReadWrite | fs.AllExecute,
	}
}

// Stat returns the FileInfo with the symlink flag from Lstat
// and the other fields from Stat like os does.
func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	return f.stat(filePath, true)
}

// Lstat returns the FileInfo of the file without following a symbolic link.
func (f *fileSystem) Lstat(filePath string) (*fs.FileInfo, error) {
	return f.stat(filePath, false)
}

// stat returns the FileInfo of the file. If follow is set, the fields
// except the symlink flag describe the file that a symbolic link points to.
func (f *fileSystem) stat(filePath string, follow bool) (*fs.FileInfo, error) {
	p, err := sharePath(filePath)
	if err != nil {
		return nil, err
	}
	var fileInfo *fs.FileInfo
	err = f.do(context.Background(), func(share *smb2.Share, _ func()) error {
		if p == "" {
			fileInfo = f.rootInfo()
			return nil
		}
		linkInfo, err := share.Lstat(p)
		if err != nil {
			return f.wrapErr(filePath, err)
		}
		info := linkInfo
		isSymlink := linkInfo.Mode()&os.ModeSymlink != 0
		if follow && isSymlink {
			info, err = share.Stat(p)
			if err != nil {
				return f.wrapErr(filePath, err)
			}
		}
		fileInfo = fs.NewFileInfo(f.file(filePath), info, strings.HasPrefix(path.Base(filePath), "."))
		fileInfo.IsSymlink = isSymlink
		return nil
	})
	return fileInfo, err
}

// ListDir lists the directory. The FileInfo of a symbolic link describes
// the link itself. The callbacks are called after the listing completed.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	p, err := sharePath(dirPath)
	if err != nil {
		return err
	}
	var infos []os.FileInfo
	err = f.do(ctx, func(share *smb2.Share, _ func()) (err error) {
		infos, err = share.ReadDir(p)
		if err != nil {
			if info, statErr := share.Stat(p); statErr == nil && !info.IsDir() {
				return fs.NewErrIsNotDirectory(f.file(dirPath))
			}
			return f.wrapErr(dirPath, err)
		}
		return nil
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
		fileInfo := fs.NewFileInfo(f.file(f.CleanPath(dirPath, info.Name())), info, strings.HasPrefix(info.Name(), "."))
		if err = callback(fileInfo); err != nil {
			return err
		}
	}
	return nil
}

// openFile opens a file handle with the flags. A created file gets
// the permissions perm if perm is not zero.
func (f *fileSystem) openFile(filePath string, flag int, perm fs.Permissions) (*file, error) {
	p, err := sharePath(filePath)
	if err != nil {
		return nil, err
	}
	var handle *file
	err = f.doHandle(func(share *smb2.Share, _ func()) error {
		smbFile, err := share.OpenFile(p, flag, perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false))
		if err != nil {
			if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
				return f.dirErr(share, filePath, p, err)
			}
			return f.wrapErr(filePath, err)
		}
		handle = &file{File: smbFile, fsys: f}
		return nil
	})
	return handle, err
}

// OpenReader returns the SMB file handle, which supports Seek and ReadAt.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	handle, err := f.openFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := handle.Stat()
	if err != nil {
		return nil, errors.Join(f.wrapErr(filePath, err), handle.Close())
	}
	if info.IsDir() {
		return nil, errors.Join(fs.NewErrIsDirectory(f.file(filePath)), handle.Close())
	}
	return handle, nil
}

func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	p, err := sharePath(filePath)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = f.do(ctx, func(share *smb2.Share, _ func()) (err error) {
		data, err = share.ReadFile(p)
		return f.dirErr(share, filePath, p, err)
	})
	return data, err
}

///////////////////////////////////////////////////////////////////////////////
// Writing

func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	return f.do(ctx, func(share *smb2.Share, _ func()) error {
		err := share.WriteFile(p, data, perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false))
		return f.dirErr(share, filePath, p, err)
	})
}

func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}

func (f *fileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, perm)
}

func (f *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.openFile(filePath, os.O_RDWR|os.O_CREATE, perm)
}

func (f *fileSystem) Truncate(ctx context.Context, filePath string, size int64) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	return f.do(ctx, func(share *smb2.Share, _ func()) error {
		return f.dirErr(share, filePath, p, share.Truncate(p, size))
	})
}

// Touch updates the modification time of an existing file
// and creates an empty file with the permissions perm otherwise.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	return f.do(context.Background(), func(share *smb2.Share, _ func()) error {
		_, err := share.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			created, err := share.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false))
			if err != nil {
				return f.wrapErr(filePath, err)
			}
			return f.wrapErr(filePath, created.Close())
		}
		if err != nil {
			return f.wrapErr(filePath, err)
		}
		now := time.Now()
		return f.wrapErr(filePath, share.Chtimes(p, now, now))
	})
}

// MakeDir creates a directory.
// It returns an error wrapping os.ErrExist if the path exists.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	p, err := sharePath(dirPath)
	if err != nil {
		return err
	}
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	if p == "" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	return f.do(context.Background(), func(share *smb2.Share, noRetry func()) error {
		noRetry()
		err := share.Mkdir(p, perm.OrDefault(fs.AllReadWrite|fs.AllExecute).FileMode(true))
		if err != nil && !errors.Is(err, os.ErrExist) && !isConnectionError(err) {
			// Some servers report a generic error for an existing path
			if _, statErr := share.Lstat(p); statErr == nil {
				return fs.NewErrAlreadyExists(f.file(dirPath))
			}
		}
		return f.wrapErr(dirPath, err)
	})
}

func (f *fileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	p, err := sharePath(dirPath)
	if err != nil {
		return err
	}
	return f.do(context.Background(), func(share *smb2.Share, _ func()) error {
		if p == "" {
			return nil
		}
		if info, err := share.Stat(p); err == nil {
			if !info.IsDir() {
				return fs.NewErrIsNotDirectory(f.file(dirPath))
			}
			return nil
		}
		return f.wrapErr(dirPath, share.MkdirAll(p, perm.OrDefault(fs.AllReadWrite|fs.AllExecute).FileMode(true)))
	})
}

// SetPermissions sets the read-only attribute of a file when perm has no
// user write bit and clears it otherwise. SMB has no POSIX permission bits.
func (f *fileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	return f.do(context.Background(), func(share *smb2.Share, _ func()) error {
		return f.wrapErr(filePath, share.Chmod(p, perm.FileMode(false)))
	})
}

// Move renames a file or directory server-side.
//
// The SMB rename replaces any existing destination, also an empty
// directory, so Move checks the destination first: an existing directory
// is an error wrapping os.ErrExist, an existing file is replaced if the
// source is a file and an fs.ErrIsNotDirectory if it is a directory.
// Moving a directory into itself is an error wrapping os.ErrInvalid.
// The checks and the rename are not atomic.
//
// A symbolic link at filePath is moved as link, not what it points to.
// Moving it onto the file it points to is an error wrapping os.ErrInvalid
// like the "are the same file" of mv, because the rename would replace the
// file with a link to itself, see isLinkOntoTarget. go-smb2 reports no link
// count, so unlike LocalFileSystem.Move, a move onto another hard link of
// the file is refused too. Servers that follow symbolic links themselves,
// like Samba without the SMB3 POSIX extensions, report a link as the file
// it points to, so Move can't tell them apart and leaves it to the server.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	src, err := sharePath(filePath)
	if err != nil {
		return err
	}
	dest, err := sharePath(destPath)
	if err != nil {
		return err
	}
	if src == dest {
		if f.closed.Load() {
			return fs.ErrFileSystemClosed
		}
		return ctx.Err()
	}
	return f.do(ctx, func(share *smb2.Share, noRetry func()) error {
		srcInfo, err := share.Lstat(src)
		if err != nil {
			return f.wrapErr(filePath, err)
		}
		if srcInfo.IsDir() && (src == "" || strings.HasPrefix(dest, src+Separator)) {
			return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, f.file(filePath), f.file(destPath))
		}
		destInfo, err := share.Lstat(dest)
		switch {
		case err == nil && destInfo.IsDir():
			return fs.NewErrAlreadyExists(f.file(destPath))
		case err == nil && srcInfo.IsDir():
			return fs.NewErrIsNotDirectory(f.file(destPath))
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return f.wrapErr(destPath, err)
		}
		if err == nil && srcInfo.Mode()&os.ModeSymlink != 0 {
			onTarget, err := isLinkOntoTarget(share, src, dest, destInfo)
			if err != nil {
				return f.wrapErr(filePath, err)
			}
			if onTarget {
				return fmt.Errorf("%w: %s and %s are the same file", os.ErrInvalid, f.file(filePath), f.file(destPath))
			}
		}
		noRetry()
		return f.wrapErr(filePath, share.Rename(src, dest))
	})
}

// linkResolver is the part of smb2.Share that isLinkOntoTarget uses,
// so that its decision can be tested without a server.
type linkResolver interface {
	Readlink(name string) (string, error)
	Stat(name string) (os.FileInfo, error)
}

// isLinkOntoTarget reports whether the symbolic link at the share path link
// may point to the file at the share path dest with the Lstat info destInfo,
// the destination of a move of the link. Renaming the link onto that file
// would remove the file and leave a link that points to itself.
//
// The link points to dest if its target resolves to dest, see linkTargetPath,
// compared without case like most servers do, or if the file that Stat of
// the link reaches through every link on the way has the attributes and
// times of destInfo, see maybeSameAttrs. go-smb2 reports no link count that
// could tell that other hard links keep the file, so such a file is taken
// for dest too. A destination that is a symbolic link is never the file
// that Stat reaches. An error of reading the link or its target, like for
// a dangling link, is no link onto dest, except for a broken connection,
// an expired session or a canceled context, which is returned.
func isLinkOntoTarget(share linkResolver, link, dest string, destInfo os.FileInfo) (bool, error) {
	if destInfo.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	target, err := share.Readlink(link)
	if isLinkCheckAborted(err) {
		return false, err
	}
	if err == nil {
		p, foreign := linkTargetPath(link, target)
		if strings.EqualFold(p, dest) || (foreign && hasPathSuffixFold(p, dest)) {
			return true, nil
		}
	}
	info, err := share.Stat(link)
	if isLinkCheckAborted(err) {
		return false, err
	}
	return err == nil && maybeSameAttrs(info, destInfo), nil
}

// isLinkCheckAborted reports whether err of a request of isLinkOntoTarget
// aborts the check, because the server could not answer.
func isLinkCheckAborted(err error) bool {
	return isConnectionError(err) || isSessionExpired(err) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// linkTargetPath returns the share path that target, the target of the
// symbolic link at the share path link as go-smb2 returns it with the
// separator `\`, points to. A relative target is resolved against the
// directory of the link and starts with ".." if it is outside of the share.
// A target with a leading separator is absolute on the share, like for
// CreateSymbolicLink. foreign is set for a target with a drive like
// `C:\data\x` or a UNC path like `\\nas\data\x` of a Windows server,
// which is returned cleaned with the separator "/", because the local
// path of the share on the server is unknown.
func linkTargetPath(link, target string) (p string, foreign bool) {
	target = strings.ReplaceAll(target, `\`, Separator)
	switch {
	case strings.HasPrefix(target, "//") || (len(target) >= 2 && target[1] == ':'):
		return path.Clean(target), true
	case strings.HasPrefix(target, Separator):
		return strings.Trim(path.Clean(target), Separator), false
	}
	p = path.Join(path.Dir(link), target)
	if p == "." {
		return "", false
	}
	return p, false
}

// hasPathSuffixFold reports whether the path p ends with the names
// of the share path dest, compared without case.
func hasPathSuffixFold(p, dest string) bool {
	i := len(p) - len(dest)
	return dest != "" && i > 0 && p[i-1] == '/' && strings.EqualFold(p[i:], dest)
}

// maybeSameAttrs reports whether the infos a and b may describe the same
// file. go-smb2 reports no file id for Share.Stat and Share.Lstat, so equal
// attributes and times are taken for the same file. SMB times have a
// resolution of 100 nanoseconds, so that two files rarely look the same.
func maybeSameAttrs(a, b os.FileInfo) bool {
	if a.Mode() != b.Mode() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	aStat, aOK := a.Sys().(*smb2.FileStat)
	bStat, bOK := b.Sys().(*smb2.FileStat)
	return !aOK || !bOK ||
		(aStat.FileAttributes == bStat.FileAttributes &&
			aStat.CreationTime.Equal(bStat.CreationTime) &&
			aStat.ChangeTime.Equal(bStat.ChangeTime))
}

// Remove removes a file or an empty directory.
// A non-empty directory is an fs.ErrDirectoryNotEmpty.
// The root directory is never removed, that is an error wrapping os.ErrInvalid.
func (f *fileSystem) Remove(filePath string) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	if p == "" {
		return fmt.Errorf("%w: smbfs: can't remove the root directory of %s", os.ErrInvalid, f)
	}
	return f.do(context.Background(), func(share *smb2.Share, noRetry func()) error {
		noRetry()
		return f.wrapErr(filePath, share.Remove(p))
	})
}

// RemoveAll removes a file or directory tree. A missing path is not an error.
// The root directory is never removed, that is an error wrapping os.ErrInvalid.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	p, err := sharePath(filePath)
	if err != nil {
		return err
	}
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	if p == "" {
		return fmt.Errorf("%w: smbfs: can't remove the root directory of %s", os.ErrInvalid, f)
	}
	return f.do(ctx, func(share *smb2.Share, _ func()) error {
		return f.wrapErr(filePath, fs.RemoveErrDoesNotExist(share.RemoveAll(p)))
	})
}

// CreateSymbolicLink creates a symbolic link at linkPath to targetPath,
// which is stored as given, with the separator of SMB. A target with
// a backslash is an error wrapping os.ErrInvalid like such a path,
// and so is a relative target that SMB would store as an absolute path:
// one that starts with a drive like "C:x" or "C:/x", also after "./"
// elements, one that starts with "./" followed by "/", like ".//x",
// and the targets "." and "./" that go-smb2 can't store.
// Most servers reject symbolic links over SMB (Samba without unix
// extensions, Windows without privileges), which is an fs.ErrUnsupported.
func (f *fileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if err := checkLinkTarget(targetPath); err != nil {
		return err
	}
	p, err := sharePath(linkPath)
	if err != nil {
		return err
	}
	return f.do(context.Background(), func(share *smb2.Share, noRetry func()) error {
		noRetry()
		err := share.Symlink(targetPath, p)
		if err == nil || isConnectionError(err) {
			return err
		}
		err = f.wrapErr(linkPath, err)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) {
			return err
		}
		return fmt.Errorf("%w: %w", fs.NewErrUnsupported(f, "CreateSymbolicLink"), err)
	})
}

// ReadSymbolicLink returns the target of the symbolic link
// at linkPath as stored, with the separator "/".
func (f *fileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	p, err := sharePath(linkPath)
	if err != nil {
		return "", err
	}
	err = f.do(context.Background(), func(share *smb2.Share, _ func()) error {
		target, err := share.Readlink(p)
		if err != nil {
			return f.wrapErr(linkPath, err)
		}
		targetPath = strings.ReplaceAll(target, `\`, Separator)
		return nil
	})
	return targetPath, err
}

// Close unregisters the file system, aborts running operations, unmounts
// the share, logs off and closes the connection. Afterwards every method
// returns fs.ErrFileSystemClosed. Calling Close more than once is a no-op.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	fs.Unregister(f)
	f.cancel(fs.ErrFileSystemClosed)

	f.mtx.Lock()
	s := f.sess
	f.sess = nil
	f.mtx.Unlock()
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.cfg.timeout())
	defer cancel()
	return s.close(ctx)
}
