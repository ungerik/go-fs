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
// RemoveAll and Move act on a link itself.
// Servers with MLST support describe a link with the facts of its target
// or with a type that the FTP client library reads as a file, so ftpfs
// can't tell their links from files and directories. FTP has no command
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
	if !f.cfg.opts.AllowBackslashInNames && strings.Contains(filePath, `\`) {
		return fmt.Errorf("%w: ftpfs path %q contains a backslash, which FTP servers on Windows take for a separator, see Options.AllowBackslashInNames", os.ErrInvalid, filePath)
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
// for servers without MLST support, which list links as such.
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
	dir, name := path.Split(filePath)
	entries, err := c.List(path.Clean(dir))
	if err == nil {
		for _, entry := range entries {
			if entry.Name == name {
				return f.entryToFileInfo(entry, filePath), entry.Target, nil
			}
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
	link, linkTarget, err := f.lstat(c, filePath)
	if err != nil || !link.IsSymlink {
		return link, err
	}
	info, current := link, filePath
	for hops := 0; info.IsSymlink && linkTarget != ""; hops++ {
		if hops == maxLinkHops {
			return nil, fmt.Errorf("ftpfs: too many levels of symbolic links: %s", link.File)
		}
		current = resolveLinkTarget(current, linkTarget)
		if err = checkCommandArg("link target", current, false); err != nil {
			return nil, err
		}
		info, linkTarget, err = f.lstat(c, current)
		if isNotExist(err) {
			return nil, fs.NewErrDoesNotExist(link.File)
		}
		if err != nil {
			return nil, err
		}
	}
	followed := *info
	followed.File = link.File
	followed.Name = link.Name
	followed.IsHidden = link.IsHidden
	followed.IsSymlink = true
	return &followed, nil
}

// resolveLinkTarget returns the path that the target of the symbolic link
// at linkPath refers to: an absolute target cleaned and a relative one
// joined to the directory of the link. The resolution is lexical, so a ".."
// of the target goes up from the path of the link even if that path
// contains links, where the server goes up from the directory they point to.
func resolveLinkTarget(linkPath, linkTarget string) string {
	if path.IsAbs(linkTarget) {
		return path.Clean(linkTarget)
	}
	return path.Join(path.Dir(linkPath), linkTarget)
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
		// Listing a file returns the file itself on many servers
		if len(entries) == 1 && entries[0].Type != ftp.EntryTypeFolder && entries[0].Name == path.Base(dirPath) {
			info, statErr := f.stat(c, dirPath)
			if statErr == nil && !info.IsDir {
				return fs.NewErrIsNotDirectory(f.JoinCleanFile(dirPath))
			}
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
		return f.walk(ctx, c, dirPath, patterns, &infos)
	}, dirPath)
	if err != nil {
		return err
	}
	return callInfos(ctx, infos, callback)
}

// walk appends the infos of the files below dirPath that match the patterns.
func (f *fileSystem) walk(ctx context.Context, c *conn, dirPath string, patterns []string, infos *[]*fs.FileInfo) error {
	entries, err := c.List(dirPath)
	if err != nil {
		return fmt.Errorf("ftpfs: listing %s: %w", f.JoinCleanFile(dirPath), err)
	}
	for _, entry := range entries {
		if entry.Name == "." || entry.Name == ".." {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := path.Join(dirPath, entry.Name)
		if entry.Type == ftp.EntryTypeFolder {
			if err := f.walk(ctx, c, entryPath, patterns, infos); err != nil {
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
// A symbolic link at either path is renamed or replaced itself, like by os.Rename.
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
		srcInfo, _, err := f.lstat(c, srcPath)
		if err != nil {
			return err
		}
		if srcInfo.IsDir && (srcPath == Separator || strings.HasPrefix(dest, srcPath+Separator)) {
			return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, srcInfo.File, f.JoinCleanFile(dest))
		}
		destInfo, _, err := f.lstat(c, dest)
		replace := err == nil
		switch {
		case replace && destInfo.IsDir:
			return fs.NewErrAlreadyExists(destInfo.File)
		case replace && srcInfo.IsDir:
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
		// The source vanished since it was checked
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
func (f *fileSystem) Remove(filePath string) error {
	return f.do(context.Background(), func(c *conn) error {
		filePath := path.Clean(filePath)
		if isRoot(filePath) {
			return fmt.Errorf("%w: ftpfs: can't remove the root directory of %s", os.ErrInvalid, f)
		}
		info, _, err := f.lstat(c, filePath)
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
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return f.do(ctx, func(c *conn) error {
		filePath := path.Clean(filePath)
		if isRoot(filePath) {
			return fmt.Errorf("%w: ftpfs: can't remove the root directory of %s", os.ErrInvalid, f)
		}
		info, _, err := f.lstat(c, filePath)
		if isNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir {
			return removeDirRecursive(c, filePath)
		}
		return c.Delete(filePath)
	}, filePath)
}

// removeDirRecursive removes the directory dirPath with its content.
func removeDirRecursive(c *conn, dirPath string) error {
	entries, err := c.List(dirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name == "." || entry.Name == ".." {
			continue
		}
		entryPath := path.Join(dirPath, entry.Name)
		if entry.Type == ftp.EntryTypeFolder {
			err = removeDirRecursive(c, entryPath)
		} else {
			err = c.Delete(entryPath)
		}
		if err != nil {
			return err
		}
	}
	return c.RemoveDir(dirPath)
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
