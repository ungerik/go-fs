// Package ftpfs implements an FTP and FTPS client file system.
//
// A file system dialed with Dial, DialAndRegister or EnsureRegistered keeps
// one control connection that is used by one operation at a time. If the
// connection breaks, the next operation reconnects. An operation that fails
// because the connection broke is retried once on a new connection, unless
// it may already have changed something on the server: after a command that
// must not run twice, like APPE, DELE, MKD, RMD or RNTO, was sent, the
// operation returns the connection error instead of repeating the command,
// because only its reply may have been lost. RemoveAll is retried, because
// repeating it removes nothing that should be kept. OpenReader dials a dedicated connection for the transfer, so a
// streaming read never blocks other operations.
//
// Operations with a context are aborted when the context is done, and Close
// aborts running operations. A connection is not used again after an abort.
//
// URIs have the form "ftp://user@host:port/path" or "ftps://user@host:port/path"
// and never contain a password; the port is omitted if it is 21. There is no
// file system registered for the plain "ftp://" and "ftps://" prefixes, so a
// URI with a password like "ftp://user:password@host/path" doesn't resolve.
// Use EnsureRegistered with the address "ftp://user:password@host" and the
// URIs "ftp://user@host/path" instead.
//
// "ftps://" uses explicit TLS (AUTH TLS) on port 21 by default and
// implicit TLS if the port is 990. The server certificate is verified
// unless Options.InsecureSkipVerify is set.
//
// Servers without MLST support list symbolic links like "ls -l" with their
// target. Stat follows such a link like os.Stat: it resolves the listed
// target relative to the directory of the link and describes the file or
// directory it points to, with IsSymlink set. ListDir and ListDirRecursive
// describe a link itself, with IsSymlink set and neither IsDir nor
// IsRegular, because a listing doesn't tell what a link points to.
// ListDirRecursive follows only a link at the listed path, and Remove,
// RemoveAll and Move act on a link itself. The FTP client library cuts the
// line of a link at the first " -> " into its name and its target, so
// ListDir and ListDirRecursive name the link "a -> b" "a", and RemoveAll of
// a directory with such a link fails. Stat, Remove, RemoveAll and Move find
// the link "a -> b", but they also take the link "a" to "b -> c" for a link
// "a -> b" to "c", because both have the same line. Servers among them that
// expand glob characters in the path argument of LIST, like wu-ftpd and
// ProFTPD with UseGlobbing but without mod_facts, which provides MLST, can
// list another directory for a path with '*', '?' or '[': then ftpfs
// describes the entries of that directory, and RemoveAll can take a link to
// a directory for a directory in it and delete the files that the link
// points to. RemoveAll refuses a directory whose name its listing lists
// more than once, like for a file whose name contains a line feed followed
// by a forged line. Servers among them that hide the names that begin with
// a dot from LIST, like vsftpd with force_dot_files=NO, its default, hide
// them from ftpfs too: Stat reports such a file as missing, ListDir,
// ListDirRecursive and copies skip it, and RemoveAll of its directory fails.
// Servers with MLST support describe a link with the facts of its target
// or with a type that the FTP client library reads as a file, so ftpfs
// can't tell their links from files and directories: ListDir describes
// a link to a directory as a directory and ListDirRecursive lists the
// files below it. RemoveAll deletes such a link itself, because on these
// servers it tries DELE for a directory before it deletes its content,
// which deletes a link and fails for a directory, one extra command per
// directory. DELE also fails for a link that the server refuses to delete,
// so after a failed DELE RemoveAll deletes the content only if LIST of the
// parent directory, which these servers answer like "ls -l", lists the
// path once and as a directory, not as a link, and else returns an error.
// The FTP client library lists with MLSD on a connection with MLST
// support, so RemoveAll of a directory dials a second connection for LIST.
// For every directory whose DELE failed it changes to the parent directory
// with CWD and lists it with LIST without a path argument, because some
// servers expand glob characters like '[' in that argument, and it is
// dialed again once for a listing if it breaks, like when the server closed
// it while it was idle. No listing is kept for the next directory, so
// RemoveAll doesn't delete through a directory that another client replaced
// with a link while RemoveAll removed the directories next to it; only
// a replacement in the time from the LIST until the content is deleted, at
// least one round trip, goes unnoticed, because every FTP command resolves
// the path again. The library sends LIST with the option -a, which servers
// that don't parse the options of LIST take for a path, so that they list
// the entry -a of the directory instead: RemoveAll doesn't use the listing
// of a directory that has an entry named -a, which it checks once per
// directory with MLST, and fails for the directories in such a directory.
// The FTP client library drops a line of a LIST listing that it can't
// parse without an error, like one whose year doesn't have four digits,
// so a client that can write into the directory and set the modification
// time of its link to such a year can hide the line of the link, and with
// a file whose name contains a line feed followed by a line that lists
// a directory with the name of the link, make the check confirm the link
// and RemoveAll delete the files that it points to.
// FTP servers on Windows (IIS with listings in the DOS format, and
// FileZilla Server or IIS with listings in the Unix format, which list
// junctions and links to directories as directories) and ProFTPD with
// ShowSymlinks off list a link to a directory as a directory with LIST too,
// so RemoveAll can't tell them apart there and deletes the files that such
// a link points to, as before v1.0.
// Remove of a link to a directory fails like for a directory, with an
// fs.ErrDirectoryNotEmpty or the error of RMD, and deletes nothing, and
// Move onto such a link is refused with an error wrapping os.ErrExist.
// MLST follows links, so Stat reports a dangling link or a cycle of links
// as not existing and ListDir can describe it as a file, but Remove and
// RemoveAll delete it and Move renames it, because they try DELE or the
// rename for a path that MLST reports as missing. If DELE fails for such
// a path, Remove and RemoveAll return the error of DELE if LIST of its
// parent directory on a second connection lists the path, and report it
// as missing otherwise, also if the directory can't be listed, unlike the
// directory check of RemoveAll, which fails without a listing.
// Writes into a link to a directory on these servers write into
// the directory that it points to: fs.CopyRecursive and the generic Move
// can't tell that a destination directory is a link. FTP has no command
// to create a link, so the file system doesn't implement
// fs.SymbolicLinkFileSystem: fs.CopyRecursive copies a link to a file as
// a file and fails with an error wrapping errors.ErrUnsupported for a link
// to a directory before it writes anything.
package ftpfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix is the URI scheme prefix of unencrypted FTP file systems.
	Prefix = "ftp://"
	// PrefixTLS is the URI scheme prefix of FTP file systems using TLS.
	PrefixTLS = "ftps://"
	// Separator is the path separator of FTP file systems.
	Separator = "/"

	// DefaultPermissions reported for FTP files.
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for FTP directories.
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllExecute
)

// Compile-time interface checks
var (
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ReadAllFileSystem          = new(fileSystem)
	_ fs.WriteAllFileSystem         = new(fileSystem)
	_ fs.AppendFileSystem           = new(fileSystem)
	_ fs.AppendWriterFileSystem     = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.TouchFileSystem            = new(fileSystem)
	_ fs.MoveFileSystem             = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
)

type fileSystem struct {
	fsimpl.PathHelper
	cfg *dialConfig

	closed  atomic.Bool
	closing context.Context // canceled by Close to abort running operations
	cancel  context.CancelCauseFunc

	sem  chan struct{} // held by the operation that uses conn
	conn *conn         // the shared control connection, nil if not connected; guarded by sem
}

func newFileSystem(cfg *dialConfig) *fileSystem {
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: cfg.prefix, Rooted: true},
		cfg:        cfg,
		sem:        make(chan struct{}, 1),
	}
	if cfg.port == defaultPort {
		// URIs with the default port 21 are accepted as well
		f.AltPrefixes = []string{cfg.prefix + ":" + defaultPort}
	}
	f.closing, f.cancel = context.WithCancelCause(context.Background())
	return f
}

// Dial dials a new FTP or FTPS connection and returns
// its file system without registering it.
//
// The address has the form [ftp[s]://][user[:password]@]host[:port];
// without a scheme "ftp://" is used, without a port 21. See Options for
// the credentials; without a username the login is anonymous.
// A '/', '?' or '#' of the username or password must be percent-encoded,
// like "%2F" for '/'. A path, query or fragment of the address is ignored,
// but an '@' in it must be percent-encoded too ("%40"): an address with
// an '@' after the host is rejected, because that '@' can be the end
// of a password with an unencoded '/', '?' or '#'.
// The URI prefix of the file system is the address without the password
// and without the default port 21, like "ftp://user@host".
//
// The context limits the dial including the TLS handshake,
// the server greeting and the login, see also Options.Timeout.
func Dial(ctx context.Context, address string, opts *Options) (fs.FileSystem, error) {
	cfg, err := parseAddress(address, opts)
	if err != nil {
		return nil, err
	}
	return dialFileSystem(ctx, cfg)
}

// dialFileSystem dials the first connection of a new file system.
func dialFileSystem(ctx context.Context, cfg *dialConfig) (fs.FileSystem, error) {
	c, err := dial(ctx, cfg, cfg.username, cfg.password)
	if err != nil {
		return nil, err
	}
	// The connection may be idle for a long time until the first operation
	c.fresh = false
	f := newFileSystem(cfg)
	f.conn = c
	return f, nil
}

// DialAndRegister dials a new FTP or FTPS connection like Dial and
// registers its file system, so that fs.File URIs with its prefix use it.
// It returns an error wrapping fs.ErrAlreadyRegistered if another
// file system is registered with the same prefix.
// Close the file system with fs.GetFileSystemByPrefixOrNil(prefix).Close()
// or use EnsureRegistered, which returns a free function.
func DialAndRegister(ctx context.Context, address string, opts *Options) error {
	fileSystem, err := Dial(ctx, address, opts)
	if err != nil {
		return err
	}
	if err = fs.Register(fileSystem); err != nil {
		return errors.Join(err, fileSystem.Close())
	}
	return nil
}

// EnsureRegistered makes sure that a file system for the address is
// registered, see Dial for the address. The first call dials and registers
// it, later calls for the same URI prefix share it; concurrent calls dial
// only once. Every call increases a reference count. The returned free
// function decreases it and closes and unregisters the file system after
// the last reference. free is never nil and can be called more than once.
//
// The options are only used if the file system is dialed. Options.Credentials
// is called by every EnsureRegistered call, because the username is part of
// the URI prefix.
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

// checkPath returns an error for an empty path, for a path that
// would corrupt the FTP command it is an argument of, and an error
// wrapping os.ErrInvalid for a path with a backslash unless
// Options.AllowBackslashInNames is set.
func (f *fileSystem) checkPath(filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if !f.cfg.opts.AllowBackslashInNames {
		if err := fsimpl.CheckNoBackslash("ftpfs", filePath, "which FTP servers on Windows take for a separator, see Options.AllowBackslashInNames"); err != nil {
			return err
		}
	}
	return checkCommandArg("path", filePath, false)
}

// begin checks the paths and the state for a new operation.
func (f *fileSystem) begin(ctx context.Context, paths ...string) error {
	for _, p := range paths {
		if err := f.checkPath(p); err != nil {
			return err
		}
	}
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	return ctx.Err()
}

// acquire returns the shared connection for exclusive use until release
// is called. A lost connection is dialed again with the context of the
// operation, which Close cancels, so a hung server blocks neither Close
// nor the operations waiting for the connection.
func (f *fileSystem) acquire(ctx context.Context) (*conn, error) {
	select {
	case f.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	if f.closed.Load() {
		// A Close that found the connection in use left
		// closing it to the operation that holds it next
		c := f.conn
		f.conn = nil
		<-f.sem
		if c != nil {
			_ = c.close()
		}
		return nil, fs.ErrFileSystemClosed
	}
	if f.conn == nil {
		username, password, _, err := f.cfg.credentials()
		if err == nil {
			f.conn, err = dial(ctx, f.cfg, username, password)
		}
		if err != nil {
			<-f.sem
			return nil, fmt.Errorf("ftpfs: reconnecting %s: %w", f.URIPrefix, err)
		}
	}
	return f.conn, nil
}

// release returns the connection acquired with acquire.
// A broken connection and the connection of a closed file system are closed.
func (f *fileSystem) release(c *conn) {
	c.fresh = false
	if c.broken {
		f.conn = nil
		_ = c.close()
	}
	<-f.sem
	// Checked after the connection was freed: a Close before finds it in
	// use and leaves it to release, a Close after closes it itself
	if f.closed.Load() {
		_ = f.closeIdleConn()
	}
}

// closeIdleConn closes the shared connection unless an operation holds it,
// which closes it itself when it finds the file system closed.
func (f *fileSystem) closeIdleConn() error {
	select {
	case f.sem <- struct{}{}:
		c := f.conn
		f.conn = nil
		<-f.sem
		if c != nil {
			return c.close()
		}
	default:
	}
	return nil
}

// do runs op with the shared connection. If op fails because the
// connection broke, do retries op once on a new connection, unless
// the connection was new or op called conn.beforeMutation already.
func (f *fileSystem) do(ctx context.Context, op func(c *conn) error, paths ...string) error {
	if err := f.begin(ctx, paths...); err != nil {
		return err
	}
	ctx, cancel := f.opContext(ctx)
	defer cancel()

	for attempt := 0; ; attempt++ {
		c, err := f.acquire(ctx)
		if err != nil {
			return err
		}
		fresh := c.fresh
		err = c.run(ctx, func() error { return op(c) })
		sent, broken := c.sent, c.broken
		f.release(c)
		switch {
		case err == nil || !broken || ctx.Err() != nil:
			return err
		case sent:
			return fmt.Errorf("ftpfs: the connection to %s broke after a command was sent, it may have been executed: %w", f.URIPrefix, err)
		case fresh || attempt > 0:
			return err
		}
		// The connection broke before anything was changed
		// on the server, retry once with a new connection
	}
}

// notExist maps a 550 "file unavailable" reply to an error wrapping
// os.ErrNotExist for operations where that is what the reply means.
func (f *fileSystem) notExist(filePath string, err error) error {
	if statusCode(err) == ftp.StatusFileUnavailable {
		return fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	return err
}

// readErr maps the failure reply of a RETR command for filePath.
// FTP servers send the same reply for a missing file and a directory.
func (f *fileSystem) readErr(c *conn, filePath string, err error) error {
	if !fileUnavailable(err) {
		return err
	}
	info, statErr := f.stat(c, filePath)
	switch {
	case statErr == nil && info.IsDir:
		return fs.NewErrIsDirectory(f.JoinCleanFile(filePath))
	case isNotExist(statErr) || isConnectionError(statErr):
		return statErr
	}
	return err
}

// writeErr maps the failure reply of a STOR or APPE command for filePath.
// FTP servers send the same reply for a directory and a missing parent directory.
func (f *fileSystem) writeErr(c *conn, filePath string, err error) error {
	if isConnectionError(err) || !fileUnavailable(err) {
		return err
	}
	info, statErr := f.stat(c, filePath)
	switch {
	case statErr == nil && info.IsDir:
		return fs.NewErrIsDirectory(f.JoinCleanFile(filePath))
	case isConnectionError(statErr):
		return statErr
	}
	if _, statErr = f.stat(c, path.Dir(filePath)); isNotExist(statErr) {
		return fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	return err
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *fileSystem) ID() string {
	return f.URIPrefix
}

func (f *fileSystem) Name() string {
	if f.cfg.secure {
		return "FTPS"
	}
	return "FTP"
}

// String returns the name and the URI prefix, which never contains a password.
func (f *fileSystem) String() string {
	return f.Name() + " " + f.URIPrefix
}

// JoinCleanFile joins the URI parts to a File of the file system.
func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

// maxLinkHops is the number of symbolic links that stat follows for one
// path, the limit of Linux, so that a cycle of links ends with an error.
const maxLinkHops = 40

// entryToFileInfo converts an ftp.Entry to the fs.FileInfo of filePath.
// The FileInfo of a link describes the link itself like for ListDir:
// a listing doesn't tell what a link points to, so IsDir and IsRegular
// are false, see fs.FileInfo.
func (f *fileSystem) entryToFileInfo(entry *ftp.Entry, filePath string) *fs.FileInfo {
	isDir := entry.Type == ftp.EntryTypeFolder
	name := path.Base(filePath)
	info := &fs.FileInfo{
		File:        f.JoinCleanFile(filePath),
		Name:        name,
		Exists:      true,
		IsDir:       isDir,
		IsRegular:   entry.Type == ftp.EntryTypeFile,
		IsSymlink:   entry.Type == ftp.EntryTypeLink,
		IsHidden:    strings.HasPrefix(name, "."),
		Modified:    entry.Time,
		Permissions: DefaultPermissions,
	}
	if isDir {
		info.Permissions = DefaultDirPermissions
	} else {
		info.Size = int64(entry.Size) //#nosec G115 -- int64 limit will not be exceeded in real world use cases
	}
	return info
}

func (f *fileSystem) rootInfo() *fs.FileInfo {
	return &fs.FileInfo{
		File:        f.RootDir(),
		Name:        Separator,
		Exists:      true,
		IsDir:       true,
		Permissions: DefaultDirPermissions,
	}
}

// lstat returns the FileInfo of the clean filePath without following
// a symbolic link, and the target of a link as the server listed it.
// It uses the MLST command and falls back to listing the parent directory
// for servers without MLST support, which list links as such, see
// listsName. Servers with MLST support describe a link with the facts of
// its target, see the package documentation.
func (f *fileSystem) lstat(c *conn, filePath string) (info *fs.FileInfo, linkTarget string, err error) {
	if filePath == Separator {
		return f.rootInfo(), "", nil
	}
	entry, err := c.GetEntry(filePath)
	switch {
	case err == nil:
		return f.entryToFileInfo(entry, filePath), entry.Target, nil
	case isConnectionError(err):
		return nil, "", err
	case statusCode(err) == ftp.StatusFileUnavailable:
		// The server supports MLST and the file does not exist
		return nil, "", fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	dir, name := splitParent(filePath)
	entries, err := c.List(dir)
	if err == nil {
		// Find the entry that lists name, see listsName. An entry with the
		// name itself is preferred, so that a file named "a -> b" is not
		// taken for the link "a" to "b -> <target>". The line of the link "a" to
		// "b -> c" is the line of the link "a -> b" to "c", so a listing
		// with one of them lists both names.
		var listed *ftp.Entry
		for _, entry := range entries {
			if entry.Name == name {
				listed = entry
				break
			}
			if listed == nil && listsName(entry, name) {
				listed = entry
			}
		}
		if listed != nil {
			return f.entryToFileInfo(listed, filePath), linkTargetOf(listed, name), nil
		}
		return nil, "", fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	if isConnectionError(err) {
		return nil, "", err
	}
	// Last resort for servers without listing support: SIZE works for files
	size, err := c.FileSize(filePath)
	if err != nil {
		if isConnectionError(err) {
			return nil, "", err
		}
		return nil, "", fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	return f.entryToFileInfo(&ftp.Entry{Name: name, Type: ftp.EntryTypeFile, Size: uint64(size)}, filePath), "", nil //#nosec G115 -- sizes are not negative
}

// stat returns the FileInfo of the clean filePath following symbolic
// links like Stat: for a link the FileInfo describes the file or directory
// it points to, with the File and Name of the link and IsSymlink set.
// A link whose target the server didn't list is described like by lstat.
// A link to a missing file is an error wrapping os.ErrNotExist.
func (f *fileSystem) stat(c *conn, filePath string) (*fs.FileInfo, error) {
	info, _, err := f.resolve(c, filePath)
	return info, err
}

// resolve returns the FileInfo of the clean filePath like stat
// and the path that the server listed for the file or directory
// that a symbolic link at filePath points to, else filePath.
// A listed link target with a backslash is rejected like a path
// unless Options.AllowBackslashInNames is set.
func (f *fileSystem) resolve(c *conn, filePath string) (*fs.FileInfo, string, error) {
	link, linkTarget, err := f.lstat(c, filePath)
	if err != nil || !link.IsSymlink {
		return link, filePath, err
	}
	info, current := link, filePath
	for hops := 0; info.IsSymlink && linkTarget != ""; hops++ {
		if hops == maxLinkHops {
			return nil, "", fmt.Errorf("ftpfs: too many levels of symbolic links: %s", link.File)
		}
		// An absolute target is cleaned and a relative one joined to the
		// directory of the link. The resolution is lexical, so a ".." of
		// the target goes up from the path of the link even if that path
		// contains links, where the server goes up from the directory
		// they point to.
		if path.IsAbs(linkTarget) {
			current = path.Clean(linkTarget)
		} else {
			current = path.Join(path.Dir(current), linkTarget)
		}
		if err = f.checkPath(current); err != nil {
			return nil, "", fmt.Errorf("ftpfs: target of symbolic link %s: %w", link.File, err)
		}
		info, linkTarget, err = f.lstat(c, current)
		if isNotExist(err) {
			return nil, "", fs.NewErrDoesNotExist(link.File)
		}
		if err != nil {
			return nil, "", err
		}
	}
	followed := *info
	followed.File = link.File
	followed.Name = link.Name
	followed.IsHidden = link.IsHidden
	followed.IsSymlink = true
	return &followed, current, nil
}

// Stat returns the FileInfo of filePath, following a symbolic link,
// see the package documentation for the links that ftpfs can follow.
func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	var info *fs.FileInfo
	err := f.do(context.Background(), func(c *conn) (err error) {
		info, err = f.stat(c, path.Clean(filePath))
		return err
	}, filePath)
	return info, err
}

// ListDir lists the directory. The connection is released before the
// callbacks are called, so callbacks can use the file system.
// The FileInfo of a symbolic link describes the link itself,
// with IsSymlink set and neither IsDir nor IsRegular.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	var infos []*fs.FileInfo
	err := f.do(ctx, func(c *conn) error {
		infos = nil
		dirPath := path.Clean(dirPath)
		entries, err := c.List(dirPath)
		if err != nil {
			if isConnectionError(err) {
				return err
			}
			info, statErr := f.stat(c, dirPath)
			if statErr == nil && !info.IsDir {
				return fs.NewErrIsNotDirectory(f.JoinCleanFile(dirPath))
			}
			return f.notExist(dirPath, err)
		}
		if entries, err = f.listedItself(c, dirPath, entries); err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name == "." || entry.Name == ".." {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(entry.Name, patterns)
			if err != nil {
				return err
			}
			if match {
				infos = append(infos, f.entryToFileInfo(entry, path.Join(dirPath, entry.Name)))
			}
		}
		return nil
	}, dirPath)
	if err != nil {
		return err
	}
	return callInfos(ctx, infos, callback)
}

// ListDirRecursive walks the directory tree and calls callback
// for every file (not directory) matching the patterns.
// The connection is released before the callbacks are called.
// A directory that can't be listed is an error.
// Symbolic links are passed to the callback like by ListDir and never
// followed, only a link at dirPath is, see fs.ListDirRecursiveFileSystem.
// That holds for servers that list links as such. A server with MLST
// support can describe a link to a directory like the directory, then
// its files are listed below the link, and a link to a directory that
// contains the link repeats the listing until the server fails to
// resolve the path, see the package documentation.
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	var infos []*fs.FileInfo
	err := f.do(ctx, func(c *conn) error {
		infos = nil
		dirPath := path.Clean(dirPath)
		info, err := f.stat(c, dirPath)
		if err != nil {
			return err
		}
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
		}
		entries, err := f.list(c, dirPath)
		if err != nil {
			return err
		}
		// A link at dirPath that the server listed itself is followed
		if entries, err = f.listedItself(c, dirPath, entries); err != nil {
			return err
		}
		return f.walk(ctx, c, dirPath, entries, patterns, &infos)
	}, dirPath)
	if err != nil {
		return err
	}
	return callInfos(ctx, infos, callback)
}

// listedItself returns the listing of the directory dirPath for entries,
// the listing that the server returned for dirPath, which on many servers
// is the file itself for a file and on servers that list like "ls -l" the
// link itself for a symbolic link: then the directory that the link points
// to is listed and a file is an fs.ErrIsNotDirectory.
func (f *fileSystem) listedItself(c *conn, dirPath string, entries []*ftp.Entry) ([]*ftp.Entry, error) {
	name := path.Base(dirPath)
	if len(entries) != 1 || entries[0].Type == ftp.EntryTypeFolder || !listsName(entries[0], name) {
		return entries, nil
	}
	info, linkTarget, err := f.lstat(c, dirPath)
	switch {
	case isConnectionError(err):
		return nil, err
	case err != nil || info.IsDir:
		return entries, nil
	case !info.IsSymlink:
		return nil, fs.NewErrIsNotDirectory(info.File)
	}
	// The entry is not the link itself if the server listed what the link
	// points to: the directory, or the file under the name of the link
	// like "ls -lL", which is no listing
	listedTarget := entries[0].Type != ftp.EntryTypeLink || linkTargetOf(entries[0], name) != linkTarget
	info, target, err := f.resolve(c, dirPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir || !listedTarget && target == dirPath {
		return nil, fs.NewErrIsNotDirectory(info.File)
	}
	if listedTarget {
		return entries, nil
	}
	entries, err = c.List(target)
	if err != nil {
		return nil, fmt.Errorf("ftpfs: listing %s, the target of %s: %w", f.JoinCleanFile(target), info.File, err)
	}
	return entries, nil
}

// list lists the directory dirPath for ListDirRecursive.
func (f *fileSystem) list(c *conn, dirPath string) ([]*ftp.Entry, error) {
	entries, err := c.List(dirPath)
	if err != nil {
		return nil, fmt.Errorf("ftpfs: listing %s: %w", f.JoinCleanFile(dirPath), err)
	}
	return entries, nil
}

// walk appends the infos of the files below dirPath that match the patterns,
// entries is the listing of dirPath.
func (f *fileSystem) walk(ctx context.Context, c *conn, dirPath string, entries []*ftp.Entry, patterns []string, infos *[]*fs.FileInfo) error {
	for _, entry := range entries {
		if entry.Name == "." || entry.Name == ".." {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := path.Join(dirPath, entry.Name)
		if entry.Type == ftp.EntryTypeFolder {
			sub, err := f.list(c, entryPath)
			if err != nil {
				return err
			}
			if err := f.walk(ctx, c, entryPath, sub, patterns, infos); err != nil {
				return err
			}
			continue
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name, patterns)
		if err != nil {
			return err
		}
		if match {
			*infos = append(*infos, f.entryToFileInfo(entry, entryPath))
		}
	}
	return nil
}

// callInfos calls callback for every info until an error or the context is done.
func callInfos(ctx context.Context, infos []*fs.FileInfo, callback func(*fs.FileInfo) error) error {
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := callback(info); err != nil {
			return err
		}
	}
	return nil
}

// fileReader streams a file over a dedicated connection.
type fileReader struct {
	*ftp.Response
	conn   *conn
	stop   func() bool // stops aborting the transfer when the file system is closed
	eof    bool
	closed bool
}

// Read reads from the data connection. After Close of the
// file system it returns an error wrapping fs.ErrFileSystemClosed.
func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.Response.Read(p)
	switch {
	case err == io.EOF:
		r.eof = true
	case err != nil && r.conn.isAborted():
		err = fmt.Errorf("%w: %w", fs.ErrFileSystemClosed, err)
	}
	return n, err
}

// Close closes the data connection and the dedicated control connection.
// Closing before the end of the file aborts the transfer, the failure
// reply that servers send for an aborted transfer is not an error then.
func (r *fileReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if !r.stop() {
		// Aborted by Close of the file system
		r.conn.broken = true
		r.conn.closeNetConns()
		return nil
	}
	err := r.Response.Close()
	if isConnectionError(err) {
		r.conn.broken = true
	}
	if !r.eof && statusCode(err) != 0 {
		err = nil
	}
	return errors.Join(err, r.conn.close())
}

// OpenReader streams the file with a RETR command over a dedicated
// connection that is closed with the reader. Close of the file system
// aborts the transfer.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	ctx := context.Background()
	if err := f.begin(ctx, filePath); err != nil {
		return nil, err
	}
	ctx, cancel := f.opContext(ctx)
	defer cancel()

	username, password, _, err := f.cfg.credentials()
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, f.cfg, username, password)
	if err != nil {
		return nil, err
	}
	filePath = path.Clean(filePath)
	var response *ftp.Response
	err = c.run(ctx, func() (err error) {
		response, err = c.Retr(filePath)
		if err != nil {
			return f.readErr(c, filePath, err)
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(err, c.close())
	}
	return &fileReader{
		Response: response,
		conn:     c,
		stop:     context.AfterFunc(f.closing, c.abort),
	}, nil
}

// ReadAll downloads the complete content of the file at filePath
// with a single RETR command.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	var data []byte
	err := f.do(ctx, func(c *conn) error {
		filePath := path.Clean(filePath)
		response, err := c.Retr(filePath)
		if err != nil {
			return f.readErr(c, filePath, err)
		}
		data, err = fs.ReadAll(ctx, response)
		return errors.Join(err, response.Close())
	}, filePath)
	return data, err
}

// WriteAll writes data to the file at filePath with a single STOR command,
// creating it if it does not exist or truncating it if it does exist.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	return f.do(ctx, func(c *conn) error {
		filePath := path.Clean(filePath)
		return f.writeErr(c, filePath, ignoreSuccessReply(c.Stor(filePath, bytes.NewReader(data))))
	}, filePath)
}

// Append appends data to the file at filePath with a single APPE command,
// creating it if it does not exist. It is not retried after the APPE
// command was sent, so that the data is never appended twice.
func (f *fileSystem) Append(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	return f.do(ctx, func(c *conn) error {
		filePath := path.Clean(filePath)
		if err := c.beforeMutation(); err != nil {
			return err
		}
		return f.writeErr(c, filePath, ignoreSuccessReply(c.Append(filePath, bytes.NewReader(data))))
	}, filePath)
}

// OpenWriter opens the file at filePath for writing, creating it if it does
// not exist or truncating it if it does exist.
//
// FTP has no random-access I/O, so the written bytes are buffered in memory
// and stored with a single STOR command when the returned writer is closed.
// The writer implements fs.WriteAborter.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.begin(context.Background(), filePath); err != nil {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// OpenAppendWriter opens the file at filePath for appending,
// creating it if it does not exist.
//
// The written bytes are buffered in memory and appended with a single
// APPE command when the returned writer is closed.
// The writer implements fs.WriteAborter.
func (f *fileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.begin(context.Background(), filePath); err != nil {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.Append(context.Background(), filePath, data, perm)
	}), nil
}

// OpenReadWriter downloads the file (or starts empty for a missing one)
// into a memory buffer supporting Read, Write and Seek that is stored
// with a single STOR command when closed.
func (f *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	data, err := f.ReadAll(context.Background(), filePath)
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(data, func(data []byte) error {
		return f.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// Touch updates the modification time of an existing file in place via
// the MFMT command when the server supports it, and only creates an
// empty file when it does not exist yet.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	return f.do(context.Background(), func(c *conn) error {
		filePath := path.Clean(filePath)
		_, err := f.stat(c, filePath)
		if isNotExist(err) {
			return f.writeErr(c, filePath, ignoreSuccessReply(c.Stor(filePath, bytes.NewReader(nil))))
		}
		if err != nil {
			return err
		}
		if c.IsSetTimeSupported() {
			return c.SetTime(filePath, time.Now())
		}
		return nil
	}, filePath)
}

// MakeDir creates a directory with the MKD command.
// It returns an error wrapping os.ErrExist if the path exists.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return f.do(context.Background(), func(c *conn) error {
		dirPath := path.Clean(dirPath)
		if err := c.beforeMutation(); err != nil {
			return err
		}
		err := ignoreSuccessReply(c.MakeDir(dirPath))
		if err == nil || isConnectionError(err) {
			return err
		}
		if _, _, statErr := f.lstat(c, dirPath); statErr == nil {
			return fs.NewErrAlreadyExists(f.JoinCleanFile(dirPath))
		}
		return f.notExist(dirPath, err)
	}, dirPath)
}

// Move renames filePath to destPath via the RNFR and RNTO commands.
// A symbolic link at either path is renamed or replaced itself, like by
// os.Rename. Servers with MLST support describe a link to a directory as
// the directory, so Move onto such a link is refused with an error wrapping
// os.ErrExist like onto a directory. A source that MLST reports as missing,
// which can be a dangling link, is renamed on these servers if the server
// can, see the package documentation.
//
// FTP servers differ in what a rename onto an existing path does,
// so Move checks the destination first: an existing directory is an
// error wrapping os.ErrExist, an existing file is replaced, and a
// directory can't replace a file. Moving a directory into itself is an
// error wrapping os.ErrInvalid. The checks and the rename are not atomic.
//
// If the server refuses to rename onto an existing file, the file is
// renamed to a temporary name ".gofs-replaced-*" in its directory, the
// rename is repeated, and the file is deleted afterwards or renamed back
// if the rename failed again. An error that leaves the file at its
// temporary name contains that name.
//
// When filePath and destPath resolve to the same location after path
// cleaning, Move returns nil without contacting the server, matching the
// no-op behavior required by the [fs.MoveFileSystem] contract.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	srcPath, dest := path.Clean(filePath), path.Clean(destPath)
	if srcPath == dest {
		return f.begin(ctx, filePath, destPath)
	}
	return f.do(ctx, func(c *conn) error {
		srcInfo, _, srcErr := f.lstat(c, srcPath)
		// MLST follows symbolic links, so a source that it reports as
		// missing can be a dangling link or a cycle of links, which the
		// rename moves; for a missing source it fails with srcErr
		unseen := isNotExist(srcErr) && c.hasMLST()
		if srcErr != nil && !unseen {
			return srcErr
		}
		srcIsDir := srcErr == nil && srcInfo.IsDir
		if srcIsDir && (srcPath == Separator || strings.HasPrefix(dest, srcPath+Separator)) {
			return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, srcInfo.File, f.JoinCleanFile(dest))
		}
		destInfo, _, err := f.lstat(c, dest)
		replace := err == nil
		switch {
		case replace && destInfo.IsDir && unseen:
			// Like os.Rename of a missing source, a link
			// can't replace a directory either
			return srcErr
		case replace && destInfo.IsDir:
			return fs.NewErrAlreadyExists(destInfo.File)
		case replace && srcIsDir:
			return fs.NewErrIsNotDirectory(destInfo.File)
		case err != nil && !isNotExist(err):
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = c.beforeMutation(); err != nil {
			return err
		}
		err = c.Rename(srcPath, dest)
		if err != nil && replace && fileUnavailable(err) {
			// Some servers don't rename onto an existing file
			return f.replaceFile(c, srcPath, dest, err)
		}
		return f.notExist(srcPath, err)
	}, filePath, destPath)
}

// replaceFile renames the file srcPath onto the existing file dest for
// servers that refused to rename it onto an existing file with renameErr.
// That failure reply doesn't tell whether the server refused the RNFR
// command for the source or the RNTO command for the destination, so the
// destination is never deleted before the source took its place: it is
// renamed to a temporary name in its directory and deleted after the
// source was renamed onto its path, or renamed back if that failed.
func (f *fileSystem) replaceFile(c *conn, srcPath, dest string, renameErr error) error {
	if _, _, err := f.lstat(c, srcPath); err != nil {
		// The source vanished since it was checked, or it is a source that
		// MLST can't see, like a dangling link, which can't replace a file
		// on a server that doesn't rename onto one
		return err
	}
	tmp := path.Join(path.Dir(dest), ".gofs-replaced-"+rand.Text())
	if err := c.Rename(dest, tmp); err != nil {
		return errors.Join(renameErr, err)
	}
	if err := c.Rename(srcPath, dest); err != nil {
		if restoreErr := c.Rename(tmp, dest); restoreErr != nil {
			return fmt.Errorf("ftpfs: moving %s to %s: %w; the replaced file remains at %s: %w", f.JoinCleanFile(srcPath), f.JoinCleanFile(dest), err, f.JoinCleanFile(tmp), restoreErr)
		}
		return err
	}
	if err := c.Delete(tmp); err != nil {
		return fmt.Errorf("ftpfs: moved %s to %s, but the replaced file remains at %s: %w", f.JoinCleanFile(srcPath), f.JoinCleanFile(dest), f.JoinCleanFile(tmp), err)
	}
	return nil
}

// isRoot reports if filePath is the root directory, also for relative
// paths like "" or ".", which the server resolves to the login directory.
func isRoot(filePath string) bool {
	return path.Clean(Separator+filePath) == Separator
}

// Remove deletes a file or a symbolic link with the DELE command
// or an empty directory with the RMD command.
// A non-empty directory is an fs.ErrDirectoryNotEmpty.
//
// Servers with MLST support describe a link to a directory as the
// directory, so Remove of such a link fails like for a directory, with an
// fs.ErrDirectoryNotEmpty or the error of RMD, and deletes nothing. A path
// that MLST reports as missing, which can be a dangling link, is deleted
// with DELE on these servers, and if that fails, Remove returns the error
// of DELE if LIST of its parent directory lists the path, and else an error
// wrapping os.ErrNotExist, also if the directory can't be listed, see the
// package documentation.
func (f *fileSystem) Remove(filePath string) error {
	return f.do(context.Background(), func(c *conn) error {
		filePath := path.Clean(filePath)
		if isRoot(filePath) {
			return fmt.Errorf("%w: ftpfs: can't remove the root directory of %s", os.ErrInvalid, f)
		}
		info, _, err := f.lstat(c, filePath)
		if isNotExist(err) && c.hasMLST() {
			notExistErr := err
			if err = c.beforeMutation(); err != nil {
				return err
			}
			r := f.newRemover(c)
			defer r.close()
			return r.deleteUnseen(filePath, notExistErr)
		}
		if err != nil {
			return err
		}
		if err = c.beforeMutation(); err != nil {
			return err
		}
		if !info.IsDir {
			return f.notExist(filePath, c.Delete(filePath))
		}
		err = c.RemoveDir(filePath)
		if fileUnavailable(err) {
			if entries, listErr := c.List(filePath); listErr == nil && hasEntries(entries) {
				return fs.NewErrDirectoryNotEmpty(info.File)
			}
		}
		return err
	}, filePath)
}

// hasEntries reports whether a listing contains other entries than "." and "..".
func hasEntries(entries []*ftp.Entry) bool {
	for _, entry := range entries {
		if entry.Name != "." && entry.Name != ".." {
			return true
		}
	}
	return false
}

// RemoveAll deletes a file or a directory with its content.
// A symbolic link is deleted itself, also below the directory,
// so the files that a link to a directory points to are kept.
// A missing path is not an error.
//
// On servers with MLST support, which describe a link to a directory
// as the directory, RemoveAll returns an error instead of deleting the
// content of a directory that LIST of its parent directory doesn't list
// once and as a directory, or whose parent directory has an entry named -a,
// and deletes a path that MLST reports as missing with DELE, which deletes
// a dangling link, see the package documentation.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return f.do(ctx, func(c *conn) error {
		filePath := path.Clean(filePath)
		if isRoot(filePath) {
			return fmt.Errorf("%w: ftpfs: can't remove the root directory of %s", os.ErrInvalid, f)
		}
		info, _, err := f.lstat(c, filePath)
		if isNotExist(err) {
			if !c.hasMLST() {
				return nil
			}
			r := f.newRemover(c)
			defer r.close()
			if err = r.deleteUnseen(filePath, err); isNotExist(err) {
				return nil
			}
			return err
		}
		if err != nil {
			return err
		}
		if !info.IsDir {
			return c.Delete(filePath)
		}
		r := f.newRemover(c)
		defer r.close()
		return r.removeFolder(filePath)
	}, filePath)
}

// remover removes a directory tree for RemoveAll and a path that MLST
// reports as missing for Remove and RemoveAll on servers with MLST support.
type remover struct {
	f   *fileSystem
	c   *conn
	ctx context.Context // of the operation, for the listing connection

	lister    *conn               // lists with LIST, dialed on first use, see list
	listerErr error               // why the listing connection can't be used
	home      string              // the working directory after the login, for relative paths
	checked   map[string]struct{} // directories without an entry named -a, see list
}

// newRemover returns a remover for the operation that runs on c,
// with the context of the operation, which Close of the file system cancels.
func (f *fileSystem) newRemover(c *conn) *remover {
	c.mtx.Lock()
	ctx := c.ctx
	c.mtx.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return &remover{f: f, c: c, ctx: ctx}
}

// close closes the listing connection.
func (r *remover) close() {
	if r.lister != nil {
		_ = r.lister.close()
		r.lister = nil
	}
}

// deleteUnseen deletes filePath with DELE after MLST reported it as missing
// with notExistErr. MLST follows symbolic links, so the path can be a
// dangling link or a cycle of links, which DELE deletes. DELE fails with
// the same reply for a missing path and for a link that the server refuses
// to delete, like in a directory without write permission, so after such
// a reply deleteUnseen returns an error wrapping the reply if LIST of the
// parent directory lists the path, see listsName, and else notExistErr,
// also if the directory can't be listed.
func (r *remover) deleteUnseen(filePath string, notExistErr error) error {
	deleteErr := r.c.Delete(filePath)
	if !fileUnavailable(deleteErr) {
		return deleteErr
	}
	dir, name := splitParent(filePath)
	entries, listErr, err := r.list(dir)
	switch {
	case err != nil:
		return err
	case listErr != nil && r.ctx.Err() != nil:
		// Not notExistErr, which RemoveAll would return as success
		return context.Cause(r.ctx)
	case listErr == nil && slices.ContainsFunc(entries, func(entry *ftp.Entry) bool { return listsName(entry, name) }):
		return fmt.Errorf("ftpfs: can't remove %s, which MLST reports as missing like a dangling symbolic link, but LIST of %s lists it: %w", r.f.JoinCleanFile(filePath), r.f.JoinCleanFile(dir), deleteErr)
	}
	return notExistErr
}

// splitParent returns the clean parent directory and the name of the clean
// filePath, which is not the root directory.
func splitParent(filePath string) (dir, name string) {
	dir, name = path.Split(filePath)
	return path.Clean(dir), name
}

// listsName reports whether entry of a LIST listing lists name. The FTP
// client library cuts the line of a symbolic link at the first " -> " into
// the name and the target of the link, so it lists the link "a -> b" with
// the name "a" and the target "b -> <target>": a link entry also lists
// name if its name, " -> " and its target start with name and " -> ".
func listsName(entry *ftp.Entry, name string) bool {
	return entry.Name == name ||
		entry.Type == ftp.EntryTypeLink && strings.HasPrefix(entry.Name+" -> "+entry.Target, name+" -> ")
}

// linkTargetOf returns the target of the symbolic link that entry of a LIST
// listing lists with name, see listsName, which is empty for no link.
func linkTargetOf(entry *ftp.Entry, name string) string {
	return strings.TrimPrefix(entry.Name+" -> "+entry.Target, name+" -> ")
}

// removeFolder removes dirPath, which the server described as a directory,
// with its content. A server with MLST support can describe a symbolic link
// to a directory like the directory, so on such a server DELE is tried
// first: it deletes a link itself and fails for a directory. Removing the
// content through a link would delete the files that it points to, and DELE
// also fails for a link that the server refuses to delete, like in a
// directory without write permission, so after a failed DELE the content is
// only removed if LIST of the parent directory confirms that dirPath is a
// directory.
func (r *remover) removeFolder(dirPath string) error {
	if r.c.hasMLST() {
		deleteErr := r.c.Delete(dirPath)
		if deleteErr == nil || isConnectionError(deleteErr) {
			return deleteErr
		}
		// dirPath is confirmed as a directory if LIST of its parent directory
		// lists it once and as a directory, else the error wraps deleteErr.
		// Servers with MLST support still answer LIST like "ls -l", which
		// lists a symbolic link as such. An entry that LIST doesn't list, or
		// a parent directory that can't be listed, is no directory either.
		// The FTP client library splits a listing into lines at every line
		// feed, so a file whose name contains a line feed followed by a line
		// that lists a directory adds an entry with any name: an entry that
		// is listed more than once is no directory either. The library also
		// cuts the line of a link at the first " -> " into the name and the
		// target of the link, so it lists the link "a -> b" with the name
		// "a", and one such added line would be the only entry named
		// "a -> b": a link entry whose name, " -> " and target start with
		// the name and " -> " lists the name too, see listsName, so that the
		// link and the added line are two entries. The parent directory is
		// listed again for every confirmation, see list.
		dir, name := splitParent(dirPath)
		parentEntries, listErr, err := r.list(dir)
		if err != nil {
			return err
		}
		if listErr != nil {
			// The listing error is not wrapped: a broken listing
			// connection is no failure of the connection of the operation
			return fmt.Errorf("ftpfs: not removing %s: DELE failed, and LIST of %s, which tells a directory from a symbolic link, failed (%v): %w", r.f.JoinCleanFile(dirPath), r.f.JoinCleanFile(dir), listErr, deleteErr)
		}
		var listed []*ftp.Entry
		for _, entry := range parentEntries {
			if listsName(entry, name) {
				listed = append(listed, entry)
			}
		}
		switch {
		case len(listed) > 1:
			return fmt.Errorf("ftpfs: not removing %s: DELE failed, and LIST of %s lists %d entries with its name, like for a file whose name contains a line feed: %w", r.f.JoinCleanFile(dirPath), r.f.JoinCleanFile(dir), len(listed), deleteErr)
		case len(listed) == 0 || listed[0].Type != ftp.EntryTypeFolder:
			return fmt.Errorf("ftpfs: not removing %s: DELE failed, and LIST of %s doesn't list it as a directory, so it may be a symbolic link to one: %w", r.f.JoinCleanFile(dirPath), r.f.JoinCleanFile(dir), deleteErr)
		}
	}
	entries, err := r.c.List(dirPath)
	if err != nil {
		return err
	}
	// The FTP client library splits a listing into lines at every line
	// feed, so a file whose name contains a line feed followed by a line
	// that lists a directory adds a directory entry with any name, like
	// the one of a symbolic link next to it, which the listing describes
	// as a link. A directory entry whose name another entry lists too,
	// see listsName, is not removed with its content, which could be the
	// content of the link's target: on servers without MLST support the
	// listing is all that tells a directory from a link.
	for _, entry := range entries {
		if entry.Type != ftp.EntryTypeFolder {
			continue
		}
		listed := 0
		for _, other := range entries {
			if listsName(other, entry.Name) {
				listed++
			}
		}
		if listed > 1 {
			return fmt.Errorf("ftpfs: not removing %s: its listing has %d entries named %q, like for a file whose name contains a line feed", r.f.JoinCleanFile(dirPath), listed, entry.Name)
		}
	}
	for _, entry := range entries {
		if entry.Name == "." || entry.Name == ".." {
			continue
		}
		entryPath := path.Join(dirPath, entry.Name)
		if entry.Type == ftp.EntryTypeFolder {
			err = r.removeFolder(entryPath)
		} else {
			err = r.c.Delete(entryPath)
		}
		if err != nil {
			return err
		}
	}
	delete(r.checked, dirPath)
	return r.c.RemoveDir(dirPath)
}

// list returns the LIST listing of the directory dirPath, or listErr if
// the directory can't be listed, and err for a failure of the connection
// of the operation, which ends the operation. The FTP client library lists
// with MLSD on a connection with MLST support, so list uses a connection of
// its own that lists with "LIST -a", which also lists hidden files. It
// changes to the directory with CWD and sends LIST without a path argument,
// because servers like ProFTPD with UseGlobbing and Pure-FTPd expand glob
// characters like '[' in that argument and some take an argument that
// starts with '-' for options, so that "LIST -a p[q]" can list another
// directory. A listing is only used if MLST finds no entry named "-a" in
// the directory, which is checked once per directory until the directory
// is removed. The connection is dialed on first use and closed by close.
// If it breaks, like when the server closed it while it was idle, it is
// dialed again once for the listing, which changes nothing on the server,
// and if that fails too, it is not used again.
//
// Every call lists the directory again, no listing is kept: another client
// can replace a directory with a symbolic link while RemoveAll removes the
// directories next to it, and a kept listing would confirm the link as the
// directory, so that RemoveAll would delete the files that the link points
// to. CWD is sent for every listing too, so that the listing connection
// resolves the path again like the commands of the operation do. A
// directory that is replaced in the time from the LIST until its content
// is deleted, at least one round trip, is not detected.
func (r *remover) list(dirPath string) (entries []*ftp.Entry, listErr, err error) {
	entries, listErr = r.listOnce(dirPath)
	if r.lister != nil && r.lister.broken && r.ctx.Err() == nil {
		r.close()
		entries, listErr = r.listOnce(dirPath)
	}
	if r.lister != nil && r.lister.broken {
		r.listerErr = errors.Join(errors.New("the connection for LIST broke"), listErr)
	}
	if listErr != nil {
		return nil, listErr, nil
	}
	if _, ok := r.checked[dirPath]; !ok {
		// The listing connection sends "LIST -a", and servers that don't
		// parse the options of LIST, like ftpserverlib with DisableLISTArgs,
		// take "-a" for a path relative to the directory and list the entry
		// "-a" instead, which a client that can write into the directory can
		// fill with entries of any name and type: an entry named "-a" is a
		// listErr. MLST on the connection of the operation checks the
		// absolute path that the listing changed to, so the working
		// directory of that connection doesn't matter. A failure of MLST
		// other than the reply for a missing path is a listErr too, and a
		// broken connection of the operation is err.
		optionPath := path.Join(r.absDir(dirPath), "-a")
		_, mlstErr := r.c.GetEntry(optionPath)
		switch {
		case mlstErr == nil:
			return nil, errors.New("the directory has an entry named -a, which servers that don't parse options of LIST would list instead"), nil
		case isConnectionError(mlstErr):
			return nil, nil, mlstErr
		case statusCode(mlstErr) != ftp.StatusFileUnavailable:
			return nil, fmt.Errorf("MLST of %s, an entry that servers that don't parse options of LIST would list instead, failed: %w", r.f.JoinCleanFile(optionPath), mlstErr), nil
		}
		if r.checked == nil {
			r.checked = make(map[string]struct{})
		}
		r.checked[dirPath] = struct{}{}
	}
	return entries, nil, nil
}

// absDir returns dirPath resolved against the working directory after
// the login, which listOnce reads with PWD for the first relative path.
func (r *remover) absDir(dirPath string) string {
	if path.IsAbs(dirPath) {
		return dirPath
	}
	return path.Join(r.home, dirPath)
}

// listOnce lists the directory dirPath on the listing connection,
// which it dials if there is none, see list.
func (r *remover) listOnce(dirPath string) ([]*ftp.Entry, error) {
	if r.listerErr != nil {
		return nil, r.listerErr
	}
	if r.lister == nil {
		username, password, _, err := r.f.cfg.credentials()
		if err == nil {
			r.lister, err = dial(r.ctx, r.f.cfg, username, password, ftp.DialWithForceListHidden(true))
		}
		if err != nil {
			r.listerErr = fmt.Errorf("dialing a connection for LIST: %w", err)
			return nil, r.listerErr
		}
	}
	var entries []*ftp.Entry
	err := r.lister.run(r.ctx, func() (err error) {
		if !path.IsAbs(dirPath) && r.home == "" {
			// The server resolves a relative path against the working
			// directory after the login, which the connection leaves with
			// its first CWD. All paths of a remover are relative if one is,
			// so its first listing reads the working directory with PWD.
			home, err := r.lister.CurrentDir()
			if err != nil {
				return err
			}
			if !path.IsAbs(home) {
				return fmt.Errorf("ftpfs: PWD replied %q, which is no absolute path to resolve %s against", home, dirPath)
			}
			r.home = home
		}
		if err = r.lister.ChangeDir(r.absDir(dirPath)); err != nil {
			return err
		}
		entries, err = r.lister.List("")
		return err
	})
	return entries, err
}

// isNotExist reports whether err wraps os.ErrNotExist.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// Close unregisters the file system and closes its connection. Running
// operations and open readers are aborted, and afterwards every method
// returns fs.ErrFileSystemClosed. Calling Close more than once is a no-op.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	fs.Unregister(f)
	f.cancel(fs.ErrFileSystemClosed)
	// A running operation is aborted and its release closes the connection
	return f.closeIdleConn()
}
