// Package ftpfs implements a FTP and FTPS client file system.
//
// A file system dialed with Dial, DialAndRegister or EnsureRegistered
// keeps one control connection that is used by one operation at a time.
// If the connection breaks, the next operation reconnects with the stored
// credentials and is retried once. OpenReader dials a dedicated connection
// for the transfer so a streaming read never blocks other operations.
//
// The package also registers file systems for the plain "ftp://" and
// "ftps://" prefixes that dial a connection per operation for URIs with
// embedded credentials like "ftp://user:password@host/path".
//
// "ftps://" uses explicit TLS (AUTH TLS) on port 21 by default and
// implicit TLS if the port is 990. The server certificate is verified
// unless Options.InsecureSkipVerify is set.
package ftpfs

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	Prefix    = "ftp://"
	PrefixTLS = "ftps://"
	Separator = "/"

	defaultPort     = "21"
	implicitTLSPort = "990"
)

var (
	// DefaultPermissions reported for FTP files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for FTP directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllExecute

	// Compile-time interface checks
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

func init() {
	// Register with prefix ftp:// and ftps:// for URLs with
	// ftp(s)://username:password@host:port schema.
	fs.Register(&fileSystem{secure: false, PathHelper: pathHelper(Prefix)})
	fs.Register(&fileSystem{secure: true, PathHelper: pathHelper(PrefixTLS)})
}

// CredentialsCallback is called by Dial to get the username and password for a FTP connection.
type CredentialsCallback func(*url.URL) (username, password string, err error)

// Password returns a CredentialsCallback that always returns
// the provided password together with the username
// from the URL that is passed to the callback.
func Password(password string) CredentialsCallback {
	return func(u *url.URL) (string, string, error) {
		return u.User.String(), password, nil
	}
}

// UsernameAndPassword returns a CredentialsCallback that always returns
// the provided username and password.
func UsernameAndPassword(username, password string) CredentialsCallback {
	return func(u *url.URL) (string, string, error) {
		return username, password, nil
	}
}

// Options for dialing FTP(S) connections. A nil *Options uses the defaults.
type Options struct {
	// TLSConfig is used for FTPS connections. If nil, a configuration
	// that verifies the server certificate for the dialed host is used.
	// A ServerName is filled in from the dialed host if empty.
	TLSConfig *tls.Config

	// InsecureSkipVerify disables the verification of the server
	// certificate, also for a non-nil TLSConfig.
	InsecureSkipVerify bool

	// DebugOut receives the FTP protocol log if not nil.
	DebugOut io.Writer
}

func (o *Options) orDefault() Options {
	if o == nil {
		return Options{}
	}
	return *o
}

type fileSystem struct {
	fsimpl.PathHelper

	mtx    sync.Mutex      // serializes the use of conn
	conn   *ftp.ServerConn // nil before the first dial and after a connection loss
	closed bool
	secure bool
	opts   Options

	// Dial arguments for reconnecting after a connection loss,
	// empty for the file systems registered for the plain prefixes
	address             string
	credentialsCallback CredentialsCallback
}

// pathHelper returns the PathHelper for a file system prefix.
// URIs with the default port 21 are accepted as well.
func pathHelper(prefix string) fsimpl.PathHelper {
	return fsimpl.PathHelper{
		URIPrefix:   prefix,
		AltPrefixes: []string{prefix + ":" + defaultPort},
		Rooted:      true,
	}
}

// Dial dials a new FTP or FTPS connection without registering it as file system.
//
// The passed address can be a URL with scheme `ftp:` or `ftps:` or just a host name.
// If no port is provided in the address, then port 21 will be used.
// The address can contain a username or a username and password.
func Dial(ctx context.Context, address string, credentialsCallback CredentialsCallback, opts *Options) (fs.FileSystem, error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nil, err
	}
	return dialPrepared(ctx, u, username, password, prefix, secure, address, credentialsCallback, opts)
}

// dialPrepared dials a file system with the results of prepareDial.
func dialPrepared(ctx context.Context, u *url.URL, username, password, prefix string, secure bool, address string, credentialsCallback CredentialsCallback, opts *Options) (fs.FileSystem, error) {
	f := &fileSystem{
		PathHelper:          pathHelper(prefix),
		secure:              secure,
		opts:                opts.orDefault(),
		address:             address,
		credentialsCallback: credentialsCallback,
	}
	var err error
	f.conn, err = f.dial(ctx, u.Host, username, password)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// DialAndRegister dials a new FTP or FTPS connection and register it as file system.
//
// The passed address can be a URL with scheme `ftp:` or `ftps:` or just a host name.
// If no port is provided in the address, then port 21 will be used.
// The address can contain a username or a username and password.
func DialAndRegister(ctx context.Context, address string, credentialsCallback CredentialsCallback, opts *Options) (fs.FileSystem, error) {
	fileSystem, err := Dial(ctx, address, credentialsCallback, opts)
	if err != nil {
		return nil, err
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

// EnsureRegistered first checks if a FTP(S) file system with the passed address
// is already registered. If not, then a new connection is dialed and registered.
// The returned free function has to be called to decrease the file system's
// reference count and close it when the reference count reaches 0.
// The returned free function will never be nil.
func EnsureRegistered(ctx context.Context, address string, credentialsCallback CredentialsCallback, opts *Options) (free func() error, err error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nop, err
	}
	if f := fs.GetFileSystemByPrefixOrNil(prefix); f != nil {
		fs.Register(f) // increase ref count
		return func() error { return f.Close() }, nil
	}

	newFS, err := dialPrepared(ctx, u, username, password, prefix, secure, address, credentialsCallback, opts)
	if err != nil {
		return nop, err
	}
	// Register dedups by prefix. If another caller registered a file system
	// with the same prefix while we were dialing, our freshly dialed connection
	// is redundant: close it and hand back a free that only drops the ref count
	// of the file system that actually won the race. The returned free is always
	// ref-count aware (it closes the connection only when the last reference is
	// released), so it can never close a file system another caller still holds.
	fs.Register(newFS)
	if registered := fs.GetFileSystemByPrefixOrNil(prefix); registered != newFS {
		_ = newFS.(*fileSystem).closeConn()
		return func() error { return registered.Close() }, nil
	}
	return func() error { return newFS.Close() }, nil
}

func prepareDial(address string, credentialsCallback CredentialsCallback) (u *url.URL, username, password, prefix string, secure bool, err error) {
	if !strings.HasPrefix(address, "ftp://") && !strings.HasPrefix(address, "ftps://") {
		if strings.Contains(address, "://") {
			return nil, "", "", "", false, fmt.Errorf("not an FTP or FTPS URL scheme: %s", address)
		}
		address = "ftp://" + address
	}
	if credentialsCallback == nil {
		return nil, "", "", "", false, errors.New("nil credentialsCallback")
	}
	u, err = url.Parse(address)
	if err != nil {
		return nil, "", "", "", false, err
	}
	if u.Scheme != "ftp" && u.Scheme != "ftps" {
		return nil, "", "", "", false, fmt.Errorf("not an FTP or FTPS URL scheme: %s", address)
	}
	// Trim default port number
	u.Host = strings.TrimSuffix(u.Host, ":"+defaultPort)

	username, password, err = credentialsCallback(u)
	if err != nil {
		return nil, "", "", "", false, err
	}
	if username == "" {
		return nil, "", "", "", false, fmt.Errorf("missing FTP username for: %s", address)
	}
	if password == "" {
		return nil, "", "", "", false, fmt.Errorf("missing FTP password for: %s", address)
	}
	prefix = fmt.Sprintf("%s://%s@%s", u.Scheme, url.User(username), u.Host)
	secure = u.Scheme == "ftps"

	return u, username, password, prefix, secure, nil
}

func nop() error { return nil }

// dial dials and logs in a new connection.
func (f *fileSystem) dial(ctx context.Context, host, username, password string) (*ftp.ServerConn, error) {
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		hostname, port = host, defaultPort
	}
	dialOptions := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithDebugOutput(f.opts.DebugOut),
		ftp.DialWithDisabledEPSV(true), // Disable EPSV to use regular PASV mode
	}
	if f.secure {
		tlsConfig := f.opts.TLSConfig.Clone()
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = hostname
		}
		if f.opts.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true //#nosec G402 -- explicitly requested via Options.InsecureSkipVerify
		}
		if port == implicitTLSPort {
			dialOptions = append(dialOptions, ftp.DialWithTLS(tlsConfig))
		} else {
			dialOptions = append(dialOptions, ftp.DialWithExplicitTLS(tlsConfig))
		}
	}
	conn, err := ftp.Dial(net.JoinHostPort(hostname, port), dialOptions...)
	if err != nil {
		return nil, err
	}
	err = conn.Login(username, password)
	if err != nil {
		return nil, errors.Join(err, conn.Quit())
	}
	return conn, nil
}

// dialStored dials a new connection with the stored dial arguments.
func (f *fileSystem) dialStored(ctx context.Context) (*ftp.ServerConn, error) {
	u, username, password, _, _, err := prepareDial(f.address, f.credentialsCallback)
	if err != nil {
		return nil, err
	}
	return f.dial(ctx, u.Host, username, password)
}

// dialURL dials a new connection with the credentials from the URL
// of filePath for the file systems registered for the plain prefixes.
func (f *fileSystem) dialURL(ctx context.Context, filePath string) (conn *ftp.ServerConn, clientPath string, err error) {
	u, err := url.Parse(f.URL(filePath))
	if err != nil {
		return nil, "", err
	}
	username := u.User.Username()
	if username == "" {
		return nil, "", fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
	}
	password, ok := u.User.Password()
	if !ok {
		return nil, "", fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
	}
	conn, err = f.dial(ctx, u.Host, username, password)
	if err != nil {
		return nil, "", err
	}
	return conn, u.Path, nil
}

// reconnectable returns true if the file system
// has the dial arguments to reconnect.
func (f *fileSystem) reconnectable() bool {
	return f.address != "" && f.credentialsCallback != nil
}

// acquire returns the connection to use exclusively until release is called.
// For a dialed file system this is the shared connection under the mutex,
// reconnected if it was lost. For the file systems registered for the plain
// prefixes a connection is dialed per call and quit by release.
func (f *fileSystem) acquire(ctx context.Context, filePath string) (conn *ftp.ServerConn, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}
	if !f.reconnectable() {
		if err = f.checkClosed(); err != nil {
			return nil, "", nop, err
		}
		conn, clientPath, err = f.dialURL(ctx, filePath)
		if err != nil {
			return nil, "", nop, err
		}
		return conn, clientPath, conn.Quit, nil
	}
	f.mtx.Lock()
	if f.closed {
		f.mtx.Unlock()
		return nil, "", nop, fs.ErrFileSystemClosed
	}
	if f.conn == nil {
		f.conn, err = f.dialStored(ctx)
		if err != nil {
			f.mtx.Unlock()
			return nil, "", nop, fmt.Errorf("FTP reconnect to %s failed: %w", f.address, err)
		}
	}
	return f.conn, filePath, func() error { f.mtx.Unlock(); return nil }, nil
}

// acquireDedicated dials a connection for a single transfer
// that is quit by the returned release function.
func (f *fileSystem) acquireDedicated(ctx context.Context, filePath string) (conn *ftp.ServerConn, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}
	if !f.reconnectable() {
		return f.acquire(ctx, filePath)
	}
	if err = f.checkClosed(); err != nil {
		return nil, "", nop, err
	}
	conn, err = f.dialStored(ctx)
	if err != nil {
		return nil, "", nop, err
	}
	return conn, filePath, conn.Quit, nil
}

// dropConn forgets and quits the shared connection if it is still conn.
func (f *fileSystem) dropConn(conn *ftp.ServerConn) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.conn == conn {
		_ = conn.Quit()
		f.conn = nil
	}
}

// do runs op with the connection. If op fails with a connection
// error on a reconnectable file system, it reconnects and retries once.
func (f *fileSystem) do(ctx context.Context, filePath string, op func(conn *ftp.ServerConn, clientPath string) error) error {
	for attempt := 0; ; attempt++ {
		conn, clientPath, release, err := f.acquire(ctx, filePath)
		if err != nil {
			return err
		}
		err = errors.Join(op(conn, clientPath), release())
		if err != nil && attempt == 0 && f.reconnectable() && isConnectionError(err) {
			f.dropConn(conn)
			continue
		}
		return err
	}
}

// isConnectionError returns true if the error indicates a connection loss
func isConnectionError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return statusCode(err) == ftp.StatusNotAvailable // 421 service not available, closing control connection
}

// statusCode returns the FTP reply code of a textproto.Error or 0.
func statusCode(err error) int {
	if e, ok := errors.AsType[*textproto.Error](err); ok {
		return e.Code
	}
	return 0
}

// ignoreSuccessReply maps errors that carry a 1xx or 2xx reply
// code to nil. Some servers reply to a completed data transfer
// in a way the client library reports as error.
func ignoreSuccessReply(err error) error {
	if code := statusCode(err); code != 0 && code < 300 {
		return nil
	}
	return err
}

// notExist maps a 550 "file unavailable" reply to an error wrapping
// os.ErrNotExist for operations where that is what the reply means.
func (f *fileSystem) notExist(filePath string, err error) error {
	if statusCode(err) == ftp.StatusFileUnavailable {
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
	if f.secure {
		return "FTPS"
	}
	return "FTP"
}

func (f *fileSystem) String() string {
	return f.URIPrefix + " file system"
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

// entryToFileInfo converts an ftp.Entry to the fs.FileInfo of filePath.
func (f *fileSystem) entryToFileInfo(entry *ftp.Entry, filePath string) *fs.FileInfo {
	isDir := entry.Type == ftp.EntryTypeFolder
	name := path.Base(filePath)
	info := &fs.FileInfo{
		File:        f.JoinCleanFile(filePath),
		Name:        name,
		Exists:      true,
		IsDir:       isDir,
		IsRegular:   !isDir,
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

// stat returns the FileInfo of clientPath. It uses the MLST command
// via GetEntry and falls back to listing the parent directory
// for servers without MLST support.
func (f *fileSystem) stat(conn *ftp.ServerConn, clientPath, filePath string) (*fs.FileInfo, error) {
	if path.Clean(clientPath) == Separator {
		return f.rootInfo(), nil
	}
	entry, err := conn.GetEntry(clientPath)
	if err == nil {
		return f.entryToFileInfo(entry, filePath), nil
	}
	if isConnectionError(err) {
		return nil, err
	}
	dir, name := path.Split(strings.TrimSuffix(clientPath, Separator))
	if dir == "" {
		dir = Separator
	}
	entries, err := conn.List(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.Name == name {
				return f.entryToFileInfo(entry, filePath), nil
			}
		}
		return nil, fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	if isConnectionError(err) {
		return nil, err
	}
	// Last resort for servers without listing support: SIZE works for files
	size, err := conn.FileSize(clientPath)
	if err != nil {
		if isConnectionError(err) {
			return nil, err
		}
		return nil, fs.NewErrDoesNotExist(f.JoinCleanFile(filePath))
	}
	return f.entryToFileInfo(&ftp.Entry{Name: name, Type: ftp.EntryTypeFile, Size: uint64(size)}, filePath), nil //#nosec G115 -- sizes are not negative
}

func (f *fileSystem) Stat(filePath string) (info *fs.FileInfo, err error) {
	err = f.do(context.Background(), filePath, func(conn *ftp.ServerConn, clientPath string) error {
		info, err = f.stat(conn, clientPath, filePath)
		return err
	})
	return info, err
}

// ListDir lists the directory. The connection is released before the
// callbacks are called, so callbacks can use the file system.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	var infos []*fs.FileInfo
	err := f.do(ctx, dirPath, func(conn *ftp.ServerConn, clientPath string) error {
		entries, err := conn.List(clientPath)
		if err != nil {
			if isConnectionError(err) {
				return err
			}
			info, statErr := f.stat(conn, clientPath, dirPath)
			if statErr == nil && !info.IsDir {
				return fs.NewErrIsNotDirectory(f.JoinCleanFile(dirPath))
			}
			return f.notExist(dirPath, err)
		}
		// Listing a file returns the file itself on many servers
		if len(entries) == 1 && entries[0].Type != ftp.EntryTypeFolder && entries[0].Name == path.Base(clientPath) {
			info, statErr := f.stat(conn, clientPath, dirPath)
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
				infos = append(infos, f.entryToFileInfo(entry, f.CleanPath(dirPath, entry.Name)))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return callInfos(ctx, infos, callback)
}

// ListDirRecursive walks the directory tree and calls callback
// for every file (not directory) matching the patterns.
// The connection is released before the callbacks are called.
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	var infos []*fs.FileInfo
	err := f.do(ctx, dirPath, func(conn *ftp.ServerConn, clientPath string) error {
		info, err := f.stat(conn, clientPath, dirPath)
		if err != nil {
			return err
		}
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
		}
		walker := conn.Walk(clientPath)
		for walker.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := walker.Err(); err != nil {
				return err
			}
			entry := walker.Stat()
			if entry.Type == ftp.EntryTypeFolder {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(entry.Name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			// The walker path is below clientPath, map it back below dirPath
			rel := strings.TrimPrefix(walker.Path(), strings.TrimSuffix(clientPath, Separator))
			infos = append(infos, f.entryToFileInfo(entry, f.CleanPath(dirPath, rel)))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return callInfos(ctx, infos, callback)
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

type fileReader struct {
	*ftp.Response
	release func() error
}

func (r *fileReader) Close() error {
	return errors.Join(r.Response.Close(), r.release())
}

// OpenReader streams the file with a RETR command
// over a dedicated connection that is quit on Close.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	conn, clientPath, release, err := f.acquireDedicated(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	response, err := conn.Retr(clientPath)
	if err != nil {
		return nil, errors.Join(f.notExist(filePath, err), release())
	}
	return &fileReader{Response: response, release: release}, nil
}

// ReadAll downloads the complete content of the file at filePath
// with a single RETR command.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) (data []byte, err error) {
	err = f.do(ctx, filePath, func(conn *ftp.ServerConn, clientPath string) error {
		response, err := conn.Retr(clientPath)
		if err != nil {
			return f.notExist(filePath, err)
		}
		data, err = fs.ReadAllContext(ctx, response)
		return errors.Join(err, response.Close())
	})
	return data, err
}

// WriteAll writes data to the file at filePath with a single STOR command,
// creating it if it does not exist or truncating it if it does exist.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	return f.do(ctx, filePath, func(conn *ftp.ServerConn, clientPath string) error {
		return f.notExist(filePath, ignoreSuccessReply(conn.Stor(clientPath, bytes.NewReader(data))))
	})
}

// Append appends data to the file at filePath with a single APPE command,
// creating it if it does not exist.
func (f *fileSystem) Append(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	return f.do(ctx, filePath, func(conn *ftp.ServerConn, clientPath string) error {
		return f.notExist(filePath, ignoreSuccessReply(conn.Append(clientPath, bytes.NewReader(data))))
	})
}

// OpenWriter opens the file at filePath for writing, creating it if it does
// not exist or truncating it if it does exist.
//
// FTP has no random-access I/O, so the written bytes are buffered in memory
// and stored with a single STOR command when the returned writer is closed.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkClosed(); err != nil {
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
func (f *fileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkClosed(); err != nil {
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
	return f.do(context.Background(), filePath, func(conn *ftp.ServerConn, clientPath string) error {
		_, err := f.stat(conn, clientPath, filePath)
		if isNotExist(err) {
			return f.notExist(filePath, ignoreSuccessReply(conn.Stor(clientPath, bytes.NewReader(nil))))
		}
		if err != nil {
			return err
		}
		if conn.IsSetTimeSupported() {
			return conn.SetTime(clientPath, time.Now())
		}
		return nil
	})
}

// MakeDir creates a directory with the MKD command.
// It returns an error wrapping os.ErrExist if the path exists.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return f.do(context.Background(), dirPath, func(conn *ftp.ServerConn, clientPath string) error {
		err := ignoreSuccessReply(conn.MakeDir(clientPath))
		if err == nil {
			return nil
		}
		if isConnectionError(err) {
			return err
		}
		if _, statErr := f.stat(conn, clientPath, dirPath); statErr == nil {
			return fs.NewErrAlreadyExists(f.JoinCleanFile(dirPath))
		}
		return f.notExist(dirPath, err)
	})
}

// Move renames filePath to destPath via the RNFR and RNTO commands.
//
// When filePath and destPath resolve to the same location after path
// cleaning, Move returns nil without contacting the server, matching the
// no-op behavior required by the [fs.MoveFileSystem] contract. (Many FTP
// servers would otherwise reject the rename with a "file unavailable"
// reply.)
func (f *fileSystem) Move(filePath string, destPath string) error {
	filePath = path.Clean(filePath)
	destPath = path.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	return f.do(context.Background(), filePath, func(conn *ftp.ServerConn, clientPath string) error {
		// clientPath is the URL path for the plain prefix file systems,
		// the destination path has the same URL form
		if clientPath != filePath {
			destPath = strings.TrimSuffix(clientPath, filePath) + destPath
		}
		return f.notExist(filePath, conn.Rename(clientPath, destPath))
	})
}

// Remove deletes a file with the DELE command
// or an empty directory with the RMD command.
func (f *fileSystem) Remove(filePath string) error {
	return f.do(context.Background(), filePath, func(conn *ftp.ServerConn, clientPath string) error {
		info, err := f.stat(conn, clientPath, filePath)
		if err != nil {
			return err
		}
		if info.IsDir {
			return conn.RemoveDir(clientPath)
		}
		return f.notExist(filePath, conn.Delete(clientPath))
	})
}

// RemoveAll deletes a file or a directory with its content.
// A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return f.do(ctx, filePath, func(conn *ftp.ServerConn, clientPath string) error {
		info, err := f.stat(conn, clientPath, filePath)
		if isNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir {
			return conn.RemoveDirRecur(clientPath)
		}
		return conn.Delete(clientPath)
	})
}

// isNotExist reports whether err wraps os.ErrNotExist.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

func (f *fileSystem) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// Close quits the underlying FTP connection and unregisters the file system.
// After the last reference is closed all methods return fs.ErrFileSystemClosed
// instead of dialing a new connection. Calling Close more than once, or on a
// file system that never connected, is a safe no-op.
// Close decreases the file system's reference count in the registry and only
// closes the underlying FTP connection once the last reference is released, so
// it never closes a connection another caller still holds.
func (f *fileSystem) Close() error {
	if !f.reconnectable() {
		return nil // the plain prefix file systems are never closed
	}
	if f.checkClosed() != nil {
		return nil
	}
	if fs.Unregister(f) > 0 {
		return nil // still referenced by another caller
	}
	return f.closeConn()
}

// closeConn closes the underlying FTP connection without touching the registry.
// It is used both by Close once the last reference is released and to discard a
// redundant connection that lost the registration race in EnsureRegistered.
func (f *fileSystem) closeConn() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.conn == nil {
		return nil
	}
	err := f.conn.Quit()
	f.conn = nil
	return err
}
