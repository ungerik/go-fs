// Package smbfs implements an SMB2/3 client file system for Windows
// shares, Samba and NAS devices using the pure Go github.com/hirochachacha/go-smb2.
//
// SMB shares have real directories and files with random access, so the
// file system implements nearly every optional interface natively:
// append and read-write handles, Truncate, Touch, MakeAllDirs,
// RemoveAll, Move (server-side rename), SetPermissions and symbolic links.
package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of smbfs URIs: smb://user@host/share
	Prefix = "smb://"

	// Separator used in smbfs paths
	Separator = "/"

	defaultPort = "445"
)

var (
	// Compile-time interface checks
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

// Options for dialing an SMB share. A nil *Options uses the defaults.
type Options struct {
	// Username and Password for NTLMv2 authentication.
	// They override credentials in the address.
	Username string
	Password string

	// Domain and Workstation are optional NTLM parameters.
	Domain      string
	Workstation string

	// Dialer for the TCP connection, nil uses a net.Dialer.
	Dialer *net.Dialer
}

type fileSystem struct {
	fsimpl.PathHelper

	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share
	closed  atomic.Bool
}

// Dial connects to an SMB share and returns its file system without
// registering it. The address has the form "[smb://][user[:password]@]host[:port]/share";
// the default port is 445. The URI prefix is "smb://user@host/share",
// or "smb://host/share" without a user.
func Dial(ctx context.Context, address string, opts *Options) (fs.FileSystem, error) {
	if !strings.Contains(address, "://") {
		address = Prefix + address
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "smb" {
		return nil, fmt.Errorf("smbfs: not an smb URL: %s", address)
	}
	share := strings.Trim(u.Path, "/")
	if u.Host == "" || share == "" || strings.Contains(share, "/") {
		return nil, fmt.Errorf("smbfs: address must be host/share: %s", address)
	}
	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	var initiator smb2.NTLMInitiator
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if opts != nil {
		if opts.Username != "" {
			username, password = opts.Username, opts.Password
		}
		initiator.Domain = opts.Domain
		initiator.Workstation = opts.Workstation
		if opts.Dialer != nil {
			dialer = opts.Dialer
		}
	}
	initiator.User, initiator.Password = username, password

	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, defaultPort)
	}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	session, err := (&smb2.Dialer{Initiator: &initiator}).DialContext(ctx, conn)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	mounted, err := session.Mount(share)
	if err != nil {
		return nil, errors.Join(err, session.Logoff(), conn.Close())
	}
	prefix := Prefix
	if username != "" {
		prefix += url.User(username).String() + "@"
	}
	prefix += strings.TrimSuffix(u.Host, ":"+defaultPort) + "/" + share
	return &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: prefix, Rooted: true},
		conn:       conn,
		session:    session,
		share:      mounted,
	}, nil
}

// DialAndRegister connects to an SMB share and registers its file system, see Dial.
func DialAndRegister(ctx context.Context, address string, opts *Options) (fs.FileSystem, error) {
	f, err := Dial(ctx, address, opts)
	if err != nil {
		return nil, err
	}
	fs.Register(f)
	return f, nil
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

func (f *fileSystem) Name() string {
	return "SMB file system"
}

func (f *fileSystem) String() string {
	return f.Name() + " with prefix " + f.URIPrefix
}

func (f *fileSystem) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}

func (f *fileSystem) checkClosed() error {
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// sharePath returns the share relative path of a file system path,
// the empty string for the root.
func sharePath(filePath string) string {
	return strings.Trim(filePath, Separator)
}

// wrapErr maps the os errors of go-smb2 to the fs error types for filePath.
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
	}
	return err
}

// shareCtx returns the share bound to ctx.
func (f *fileSystem) shareCtx(ctx context.Context) *smb2.Share {
	return f.share.WithContext(ctx)
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
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := sharePath(filePath)
	if p == "" {
		return f.rootInfo(), nil
	}
	linkInfo, err := f.share.Lstat(p)
	if err != nil {
		return nil, f.wrapErr(filePath, err)
	}
	info := linkInfo
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		info, err = f.share.Stat(p)
		if err != nil {
			return nil, f.wrapErr(filePath, err)
		}
	}
	fileInfo := fs.NewFileInfo(f.file(filePath), info, f.IsHidden(filePath))
	fileInfo.IsSymlink = linkInfo.Mode()&os.ModeSymlink != 0
	return fileInfo, nil
}

func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	infos, err := f.shareCtx(ctx).ReadDir(sharePath(dirPath))
	if err != nil {
		if info, statErr := f.Stat(dirPath); statErr == nil && !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
		}
		return f.wrapErr(dirPath, err)
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
		err = callback(fs.NewFileInfo(f.file(f.CleanPath(dirPath, info.Name())), info, f.IsHidden(info.Name())))
		if err != nil {
			return err
		}
	}
	return nil
}

// openFile opens a file with the flags. A created file gets
// the permissions perm if perm is not zero.
func (f *fileSystem) openFile(filePath string, flag int, perm fs.Permissions) (*smb2.File, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	file, err := f.share.OpenFile(sharePath(filePath), flag, perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false))
	if err != nil {
		return nil, f.wrapErr(filePath, err)
	}
	return file, nil
}

// OpenReader returns the SMB file handle, which supports Seek and ReadAt.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	file, err := f.openFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(f.wrapErr(filePath, err), file.Close())
	}
	if info.IsDir() {
		return nil, errors.Join(fs.NewErrIsDirectory(f.file(filePath)), file.Close())
	}
	return file, nil
}

func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	data, err := f.shareCtx(ctx).ReadFile(sharePath(filePath))
	return data, f.wrapErr(filePath, err)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	return f.wrapErr(filePath, f.shareCtx(ctx).WriteFile(sharePath(filePath), data, perm.OrDefault(fs.UserAndGroupReadWrite).FileMode(false)))
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

func (f *fileSystem) Truncate(filePath string, size int64) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	return f.wrapErr(filePath, f.share.Truncate(sharePath(filePath), size))
}

// Touch updates the modification time of an existing file
// and creates an empty file with the permissions perm otherwise.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := sharePath(filePath)
	_, err := f.share.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		file, err := f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		return file.Close()
	}
	if err != nil {
		return f.wrapErr(filePath, err)
	}
	now := time.Now()
	return f.wrapErr(filePath, f.share.Chtimes(p, now, now))
}

func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	p := sharePath(dirPath)
	if p == "" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	err := f.share.Mkdir(p, perm.OrDefault(fs.AllReadWrite|fs.AllExecute).FileMode(true))
	if err != nil && !errors.Is(err, os.ErrExist) {
		// Some servers report a generic error for an existing path
		if _, statErr := f.share.Lstat(p); statErr == nil {
			return fs.NewErrAlreadyExists(f.file(dirPath))
		}
	}
	return f.wrapErr(dirPath, err)
}

func (f *fileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	p := sharePath(dirPath)
	if p == "" {
		return nil
	}
	if info, err := f.share.Stat(p); err == nil {
		if !info.IsDir() {
			return fs.NewErrIsNotDirectory(f.file(dirPath))
		}
		return nil
	}
	return f.wrapErr(dirPath, f.share.MkdirAll(p, perm.OrDefault(fs.AllReadWrite|fs.AllExecute).FileMode(true)))
}

// SetPermissions sets the read-only attribute of a file when perm has no
// user write bit and clears it otherwise. SMB has no POSIX permission bits.
func (f *fileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	return f.wrapErr(filePath, f.share.Chmod(sharePath(filePath), perm.FileMode(false)))
}

// Move renames a file or directory server-side.
func (f *fileSystem) Move(filePath string, destPath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	src, dest := sharePath(filePath), sharePath(destPath)
	if src == dest {
		return nil
	}
	return f.wrapErr(filePath, f.share.Rename(src, dest))
}

// Remove removes a file or an empty directory.
func (f *fileSystem) Remove(filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := sharePath(filePath)
	if p == "" {
		return fmt.Errorf("can't remove root directory of %s", f)
	}
	return f.wrapErr(filePath, f.share.Remove(p))
}

// RemoveAll removes a file or directory tree. A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := sharePath(filePath)
	if p == "" {
		return fmt.Errorf("can't remove root directory of %s", f)
	}
	err := f.shareCtx(ctx).RemoveAll(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return f.wrapErr(filePath, err)
}

func (f *fileSystem) IsSymbolicLink(filePath string) bool {
	if f.closed.Load() || sharePath(filePath) == "" {
		return false
	}
	info, err := f.share.Lstat(sharePath(filePath))
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func (f *fileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if linkPath == "" {
		return fs.ErrEmptyPath
	}
	err := f.share.Symlink(sharePath(targetPath), sharePath(linkPath))
	if err != nil {
		err = f.wrapErr(linkPath, err)
		if isNotExist(err) {
			return err
		}
		// Most servers reject symbolic links over SMB
		// (Samba without unix extensions, Windows without privileges)
		return fmt.Errorf("%w: %w", fs.NewErrUnsupported(f, "CreateSymbolicLink"), err)
	}
	return nil
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

func (f *fileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if err := f.checkClosed(); err != nil {
		return "", err
	}
	if linkPath == "" {
		return "", fs.ErrEmptyPath
	}
	target, err := f.share.Readlink(sharePath(linkPath))
	if err != nil {
		return "", f.wrapErr(linkPath, err)
	}
	return f.CleanPath(strings.ReplaceAll(target, `\`, "/")), nil
}

// Close unmounts the share, logs off, closes the connection
// and unregisters the file system. Close is idempotent.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	fs.Unregister(f)
	err := errors.Join(f.share.Umount(), f.session.Logoff())
	_ = f.conn.Close() // Logoff already closes the connection
	return err
}
