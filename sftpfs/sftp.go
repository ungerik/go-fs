// Package sftpfs implements an SFTP client file system.
//
// A file system dialed with Dial, DialAndRegister or EnsureRegistered
// keeps one SFTP connection. If the connection breaks, the next
// operation reconnects with the stored credentials and host key callback
// and is retried once.
//
// The package also registers a file system for the plain "sftp://"
// prefix that dials a connection per operation for URIs with embedded
// credentials like "sftp://user:password@host/path". Such connections
// verify the host key with URLHostKeyCallback, which must be set before
// use.
package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	Prefix    = "sftp://"
	Separator = "/"
)

var (
	// URLHostKeyCallback verifies the host keys of servers that are
	// dialed for URIs with embedded credentials via the file system
	// registered for the plain "sftp://" prefix.
	// It is nil by default, so such URIs fail until it is set.
	// AcceptAnyHostKey disables the verification.
	URLHostKeyCallback ssh.HostKeyCallback

	// Compile-time interface checks
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

func init() {
	// Register with prefix sftp:// for URLs with
	// sftp://username:password@host:port schema.
	fs.Register(&fileSystem{PathHelper: pathHelper(Prefix)})
}

// CredentialsCallback is called by Dial to get the username and password for a SFTP connection.
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

// AcceptAnyHostKey can be passed as hostKeyCallback to Dial
// to accept any SSH public key from a remote host.
func AcceptAnyHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return nil
}

type fileSystem struct {
	fsimpl.PathHelper

	mtx    sync.RWMutex
	client *sftp.Client // nil before the first dial and after a connection loss
	closed bool

	// connLogger is optional (can be nil) and will be used
	// to log connection events like dialing and reconnecting
	connLogger fs.Logger

	// Dial arguments for reconnecting after a connection loss,
	// empty for the file system registered for the plain prefix
	address             string
	credentialsCallback CredentialsCallback
	hostKeyCallback     ssh.HostKeyCallback
}

// Dial dials a new SFTP connection without registering it as file system.
//
// The passed address can be a URL with scheme `sftp:` or just a host name.
// If no port is provided in the address, then port 22 will be used.
// The address can contain a username or a username and password.
//
// The connLogger parameter is optional (can be nil) and will be used
// to log connection events like dialing and reconnecting.
func Dial(ctx context.Context, address string, credentialsCallback CredentialsCallback, hostKeyCallback ssh.HostKeyCallback, connLogger fs.Logger) (fs.FileSystem, error) {
	u, username, password, prefix, err := prepareDial(address, credentialsCallback, hostKeyCallback)
	if err != nil {
		return nil, err
	}
	f := &fileSystem{
		PathHelper:          pathHelper(prefix),
		connLogger:          connLogger,
		address:             address,
		credentialsCallback: credentialsCallback,
		hostKeyCallback:     hostKeyCallback,
	}
	f.client, err = dialRetry(ctx, u.Host, username, password, hostKeyCallback, connLogger)
	if err != nil {
		return nil, err
	}
	f.watchConnection(f.client)
	return f, nil
}

// pathHelper returns the PathHelper for a file system prefix.
// URIs with the default SFTP port 22 are accepted as well.
func pathHelper(prefix string) fsimpl.PathHelper {
	return fsimpl.PathHelper{
		URIPrefix:   prefix,
		AltPrefixes: []string{prefix + ":22"},
		Rooted:      true,
	}
}

func prepareDial(address string, credentialsCallback CredentialsCallback, hostKeyCallback ssh.HostKeyCallback) (u *url.URL, username, password, prefix string, err error) {
	if !strings.HasPrefix(address, "sftp://") {
		if strings.Contains(address, "://") {
			return nil, "", "", "", fmt.Errorf("not an SFTP URL scheme: %s", address)
		}
		address = "sftp://" + address
	}
	if credentialsCallback == nil {
		return nil, "", "", "", errors.New("nil credentialsCallback")
	}
	if hostKeyCallback == nil {
		return nil, "", "", "", errors.New("nil hostKeyCallback")
	}
	u, err = url.Parse(address)
	if err != nil {
		return nil, "", "", "", err
	}
	if u.Scheme != "sftp" {
		return nil, "", "", "", fmt.Errorf("not an SFTP URL scheme: %s", address)
	}
	// Trim default port number
	u.Host = strings.TrimSuffix(u.Host, ":22")

	username, password, err = credentialsCallback(u)
	if err != nil {
		return nil, "", "", "", err
	}
	if username == "" {
		return nil, "", "", "", fmt.Errorf("missing SFTP username for: %s", address)
	}
	if password == "" {
		return nil, "", "", "", fmt.Errorf("missing SFTP password for: %s", address)
	}
	prefix = fmt.Sprintf("sftp://%s@%s", url.User(username), u.Host)

	return u, username, password, prefix, nil
}

// DialAndRegister dials a new SFTP connection and register it as file system.
//
// The passed address can be a URL with scheme `sftp:` or just a host name.
// If no port is provided in the address, then port 22 will be used.
// The address can contain a username or a username and password.
//
// The connLogger parameter is optional (can be nil) and will be used
// to log connection events like dialing and reconnecting.
func DialAndRegister(ctx context.Context, address string, credentialsCallback CredentialsCallback, hostKeyCallback ssh.HostKeyCallback, connLogger fs.Logger) (fs.FileSystem, error) {
	fileSystem, err := Dial(ctx, address, credentialsCallback, hostKeyCallback, connLogger)
	if err != nil {
		return nil, err
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

// EnsureRegistered first checks if a SFTP file system with the passed address
// is already registered. If not, then a new connection is dialed and registered.
// The returned free function has to be called to decrease the file system's
// reference count and close it when the reference count reaches 0.
// The returned free function will never be nil.
//
// The connLogger parameter is optional (can be nil) and will be used
// to log connection events like dialing and reconnecting.
func EnsureRegistered(ctx context.Context, address string, credentialsCallback CredentialsCallback, hostKeyCallback ssh.HostKeyCallback, connLogger fs.Logger) (free func() error, err error) {
	_, _, _, prefix, err := prepareDial(address, credentialsCallback, hostKeyCallback)
	if err != nil {
		return nop, err
	}
	if f := fs.GetFileSystemByPrefixOrNil(prefix); f != nil {
		fs.Register(f) // increase ref count
		return func() error { return f.Close() }, nil
	}

	newFS, err := Dial(ctx, address, credentialsCallback, hostKeyCallback, connLogger)
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

func nop() error { return nil }

func dial(ctx context.Context, host, user, password string, hostKeyCallback ssh.HostKeyCallback, connLogger fs.Logger) (*sftp.Client, error) {
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: hostKeyCallback,
	}
	d := net.Dialer{}
	if !strings.ContainsRune(host, ':') {
		host += ":22"
	}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, host, config)
	if err != nil {
		return nil, err
	}
	client, err := sftp.NewClient(ssh.NewClient(sshConn, chans, reqs))
	if err != nil {
		return nil, err
	}
	if connLogger != nil {
		connLogger.Printf("Dialed SFTP connection to %s with user %s", host, user)
	}
	return client, nil
}

// dialRetry dials with up to MaxConnectRetries retries
// and exponential backoff starting at InitialRetryBackoff.
func dialRetry(ctx context.Context, host, user, password string, hostKeyCallback ssh.HostKeyCallback, connLogger fs.Logger) (*sftp.Client, error) {
	backoff := InitialRetryBackoff
	for attempt := 0; ; attempt++ {
		client, err := dial(ctx, host, user, password, hostKeyCallback, connLogger)
		if err == nil {
			return client, nil
		}
		if attempt >= MaxConnectRetries || !isConnectionError(err) {
			return nil, err
		}
		if connLogger != nil {
			connLogger.Printf("Waiting %s before retry %d of %d of SFTP connection to %s: %s", backoff, attempt+1, MaxConnectRetries, host, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
}

// isConnectionError returns true if the error indicates a connection loss
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection closed") ||
		strings.Contains(errStr, "connection lost") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "ssh_msg_disconnect")
}

// watchConnection drops the client when its connection ends,
// so the next operation reconnects instead of failing.
func (f *fileSystem) watchConnection(client *sftp.Client) {
	go func() {
		_ = client.Wait()
		f.dropClient(client)
	}()
}

// dropClient forgets the client if it is still the current one.
func (f *fileSystem) dropClient(client *sftp.Client) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.client == client && !f.closed {
		f.client = nil
		if f.connLogger != nil {
			f.connLogger.Printf("Lost SFTP connection to %s", f.address)
		}
	}
}

// reconnectable returns true if the file system
// has the dial arguments to reconnect.
func (f *fileSystem) reconnectable() bool {
	return f.address != "" && f.credentialsCallback != nil && f.hostKeyCallback != nil
}

// getClient returns the SFTP client and the path to use with it.
// The returned release function is never nil and must be called
// when the client is not needed anymore.
//
// For a dialed file system the client is reconnected if the
// connection was lost. For the file system registered for the
// plain prefix a connection is dialed per call with the credentials
// from the URL and released by the release function.
func (f *fileSystem) getClient(ctx context.Context, filePath string) (client *sftp.Client, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}
	f.mtx.RLock()
	client, closed := f.client, f.closed
	f.mtx.RUnlock()
	if closed {
		return nil, "", nop, fs.ErrFileSystemClosed
	}
	if client != nil {
		return client, filePath, nop, nil
	}
	if f.reconnectable() {
		client, err = f.reconnect(ctx)
		if err != nil {
			return nil, "", nop, err
		}
		return client, filePath, nop, nil
	}

	// Dial with credentials from the URL, never with the
	// stored host key callback of another file system
	u, err := url.Parse(f.URL(filePath))
	if err != nil {
		return nil, "", nop, err
	}
	username := u.User.Username()
	if username == "" {
		return nil, "", nop, fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
	}
	password, ok := u.User.Password()
	if !ok {
		return nil, "", nop, fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
	}
	if URLHostKeyCallback == nil {
		return nil, "", nop, errors.New("sftpfs.URLHostKeyCallback must be set to dial sftp URLs with embedded credentials")
	}
	client, err = dialRetry(ctx, u.Host, username, password, URLHostKeyCallback, f.connLogger)
	if err != nil {
		return nil, "", nop, err
	}
	return client, u.Path, client.Close, nil
}

// reconnect dials a new connection with the stored dial arguments
// unless another call reconnected in the meantime.
func (f *fileSystem) reconnect(ctx context.Context) (*sftp.Client, error) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil, fs.ErrFileSystemClosed
	}
	if f.client != nil {
		return f.client, nil
	}
	if f.connLogger != nil {
		f.connLogger.Printf("Reconnecting SFTP to %s", f.address)
	}
	u, username, password, _, err := prepareDial(f.address, f.credentialsCallback, f.hostKeyCallback)
	if err != nil {
		return nil, err
	}
	client, err := dialRetry(ctx, u.Host, username, password, f.hostKeyCallback, f.connLogger)
	if err != nil {
		return nil, fmt.Errorf("SFTP reconnect to %s failed: %w", f.address, err)
	}
	f.client = client
	f.watchConnection(client)
	return client, nil
}

// do runs op with the SFTP client. If op fails with a connection
// error on a reconnectable file system, it reconnects and retries once.
func (f *fileSystem) do(ctx context.Context, filePath string, op func(client *sftp.Client, clientPath string) error) error {
	for attempt := 0; ; attempt++ {
		client, clientPath, release, err := f.getClient(ctx, filePath)
		if err != nil {
			return err
		}
		err = errors.Join(op(client, clientPath), release())
		if err != nil && attempt == 0 && f.reconnectable() && isConnectionError(err) {
			f.dropClient(client)
			continue
		}
		return err
	}
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
	return "SFTP"
}

func (f *fileSystem) String() string {
	return f.URIPrefix + " file system"
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

// stat returns the FileInfo with the symlink flag set
// from Lstat and the other fields from Stat like os does.
func (f *fileSystem) stat(client *sftp.Client, clientPath, filePath string) (*fs.FileInfo, error) {
	linkInfo, err := client.Lstat(clientPath)
	if err != nil {
		return nil, err
	}
	info := linkInfo
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		info, err = client.Stat(clientPath)
		if err != nil {
			return nil, err
		}
	}
	fileInfo := fs.NewFileInfo(f.JoinCleanFile(filePath), info, f.IsHidden(filePath))
	fileInfo.IsSymlink = linkInfo.Mode()&os.ModeSymlink != 0
	return fileInfo, nil
}

func (f *fileSystem) Stat(filePath string) (info *fs.FileInfo, err error) {
	err = f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		info, err = f.stat(client, clientPath, filePath)
		return err
	})
	return info, err
}

func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.do(ctx, dirPath, func(client *sftp.Client, clientPath string) error {
		infos, err := client.ReadDirContext(ctx, clientPath)
		if err != nil {
			// Distinguish a missing directory from a path that is not a directory
			info, statErr := client.Stat(clientPath)
			switch {
			case statErr == nil && !info.IsDir():
				return fs.NewErrIsNotDirectory(f.JoinCleanFile(dirPath))
			case errors.Is(err, os.ErrNotExist) || errors.Is(statErr, os.ErrNotExist):
				return fs.NewErrDoesNotExist(f.JoinCleanFile(dirPath))
			}
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
			err = callback(fs.NewFileInfo(f.JoinCleanFile(dirPath, info.Name()), info, f.IsHidden(info.Name())))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ListDirRecursive walks the directory tree with a single connection
// and calls callback for every file (not directory) matching the patterns.
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.do(ctx, dirPath, func(client *sftp.Client, clientPath string) error {
		if _, err := client.Stat(clientPath); err != nil {
			return err
		}
		walker := client.Walk(clientPath)
		for walker.Step() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := walker.Err(); err != nil {
				return err
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
			// The walker path is below clientPath, map it back below dirPath
			rel := strings.TrimPrefix(walker.Path(), clientPath)
			err = callback(fs.NewFileInfo(f.JoinCleanFile(dirPath, rel), info, f.IsHidden(info.Name())))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

type sftpFile struct {
	*sftp.File
	release func() error
}

func (f *sftpFile) Close() error {
	return errors.Join(f.File.Close(), f.release())
}

// openFile opens a file with the flags. A file created by the
// call gets the permissions perm if perm is not zero.
func (f *fileSystem) openFile(filePath string, flags int, perm fs.Permissions) (*sftpFile, error) {
	ctx := context.Background()
	for attempt := 0; ; attempt++ {
		client, clientPath, release, err := f.getClient(ctx, filePath)
		if err != nil {
			return nil, err
		}
		created := false
		if perm != 0 && flags&os.O_CREATE != 0 {
			_, statErr := client.Stat(clientPath)
			created = errors.Is(statErr, os.ErrNotExist)
		}
		file, err := client.OpenFile(clientPath, flags)
		if err != nil {
			err = errors.Join(err, release())
			if attempt == 0 && f.reconnectable() && isConnectionError(err) {
				f.dropClient(client)
				continue
			}
			return nil, err
		}
		if created {
			err = file.Chmod(os.FileMode(perm))
			if err != nil {
				return nil, errors.Join(err, file.Close(), release())
			}
		}
		return &sftpFile{file, release}, nil
	}
}

func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return f.openFile(filePath, os.O_RDONLY, 0)
}

func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}

func (f *fileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return f.openFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
}

func (f *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.openFile(filePath, os.O_RDWR|os.O_CREATE, perm)
}

func (f *fileSystem) Truncate(filePath string, size int64) error {
	return f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		return client.Truncate(clientPath, size)
	})
}

// Touch updates the modification time of an existing file in place
// via the SFTP SETSTAT packet and only creates an empty file
// with the permissions perm when it does not exist yet.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	return f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		_, err := client.Stat(clientPath)
		if errors.Is(err, os.ErrNotExist) {
			file, err := client.OpenFile(clientPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
			if err != nil {
				return err
			}
			if perm != 0 {
				err = file.Chmod(os.FileMode(perm))
			}
			return errors.Join(err, file.Close())
		}
		if err != nil {
			return err
		}
		now := time.Now()
		return client.Chtimes(clientPath, now, now)
	})
}

func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return f.do(context.Background(), dirPath, func(client *sftp.Client, clientPath string) error {
		err := client.Mkdir(clientPath)
		if err != nil {
			// SFTP reports a generic failure for an existing path,
			// map it to os.ErrExist like os.Mkdir does
			if _, statErr := client.Stat(clientPath); statErr == nil {
				return fs.NewErrAlreadyExists(f.JoinCleanFile(dirPath))
			}
			return err
		}
		if perm != 0 {
			return client.Chmod(clientPath, os.FileMode(perm))
		}
		return nil
	})
}

// MakeAllDirs creates the directory and all missing parents
// with a single SFTP client MkdirAll call.
func (f *fileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	return f.do(context.Background(), dirPath, func(client *sftp.Client, clientPath string) error {
		info, err := client.Stat(clientPath)
		if err == nil {
			if !info.IsDir() {
				return fs.NewErrIsNotDirectory(f.JoinCleanFile(dirPath))
			}
			return nil
		}
		err = client.MkdirAll(clientPath)
		if err != nil {
			return err
		}
		if perm != 0 {
			return client.Chmod(clientPath, os.FileMode(perm))
		}
		return nil
	})
}

func (f *fileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		return client.Chmod(clientPath, os.FileMode(perm))
	})
}

// Move renames filePath to destPath via the SFTP RENAME packet.
//
// When filePath and destPath resolve to the same location after path
// cleaning, Move returns nil without contacting the server, matching the
// no-op behavior required by the [fs.MoveFileSystem] contract. (Many SFTP
// servers would otherwise reject the rename with a "file exists" error.)
func (f *fileSystem) Move(filePath string, destPath string) error {
	filePath = path.Clean(filePath)
	destPath = path.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	return f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		// clientPath is the URL path for the plain prefix file system,
		// the destination path has the same URL form
		if clientPath != filePath {
			destPath = strings.TrimSuffix(clientPath, filePath) + destPath
		}
		return client.Rename(clientPath, destPath)
	})
}

func (f *fileSystem) Remove(filePath string) error {
	return f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		return client.Remove(clientPath)
	})
}

// RemoveAll removes the file or directory tree over one connection.
// Symbolic links are removed, not followed. A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	return f.do(ctx, filePath, func(client *sftp.Client, clientPath string) error {
		info, err := client.Lstat(clientPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return removeAll(ctx, client, clientPath, info)
	})
}

// removeAll removes path with the Lstat info recursively.
func removeAll(ctx context.Context, client *sftp.Client, path string, info os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !info.IsDir() {
		return client.Remove(path)
	}
	entries, err := client.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		err = removeAll(ctx, client, path+Separator+entry.Name(), entry)
		if err != nil {
			return err
		}
	}
	return client.RemoveDirectory(path)
}

func (f *fileSystem) IsSymbolicLink(filePath string) bool {
	var isLink bool
	err := f.do(context.Background(), filePath, func(client *sftp.Client, clientPath string) error {
		info, err := client.Lstat(clientPath)
		if err != nil {
			return err
		}
		isLink = info.Mode()&os.ModeSymlink != 0
		return nil
	})
	return err == nil && isLink
}

func (f *fileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	return f.do(context.Background(), linkPath, func(client *sftp.Client, clientPath string) error {
		return client.Symlink(targetPath, clientPath)
	})
}

func (f *fileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	err = f.do(context.Background(), linkPath, func(client *sftp.Client, clientPath string) error {
		targetPath, err = client.ReadLink(clientPath)
		return err
	})
	return targetPath, err
}

// Close closes the underlying SFTP connection and unregisters the file system.
// After the last reference is closed all methods return fs.ErrFileSystemClosed
// instead of dialing a new connection. Calling Close more than once, or on a
// file system that never connected, is a safe no-op.
// Close decreases the file system's reference count in the registry and only
// closes the underlying SFTP connection once the last reference is released, so
// it never closes a connection another caller still holds.
func (f *fileSystem) Close() error {
	f.mtx.RLock()
	alreadyClosed := f.closed || !f.reconnectable()
	f.mtx.RUnlock()
	if alreadyClosed {
		return nil // already closed or the plain prefix file system
	}
	if fs.Unregister(f) > 0 {
		return nil // still referenced by another caller
	}
	return f.closeConn()
}

// closeConn closes the underlying SFTP connection without touching the
// registry. It is used both by Close once the last reference is released and to
// discard a redundant connection that lost the registration race in
// EnsureRegistered.
func (f *fileSystem) closeConn() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.client == nil {
		return nil
	}
	err := f.client.Close()
	f.client = nil
	return err
}
