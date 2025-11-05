// Package sftpfs implements a SFTP client file system.
package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
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

func init() {
	// Register with prefix sftp:// for URLs with
	// sftp://username:password@host:port schema.
	fs.Register(&fileSystem{prefix: Prefix})
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

// // UsernameAndPasswordFromURL is a CredentialsCallback that returns
// // the username and password encoded in the passed URL.
// func UsernameAndPasswordFromURL(u *url.URL) (username, password string, err error) {
// 	password, ok := u.User.Password()
// 	if !ok {
// 		return "", "", fmt.Errorf("no password in URL: %s", u.String())
// 	}
// 	return u.User.Username(), password, nil
// }

// AcceptAnyHostKey can be passed as hostKeyCallback to Dial
// to accept any SSH public key from a remote host.
func AcceptAnyHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return nil
}

type fileSystem struct {
	prefix    string
	client    *sftp.Client
	clientMtx sync.RWMutex

	// connLogger is optional (can be nil) and will be used
	// to log connection events like dialing and reconnecting
	connLogger fs.Logger

	// Dial arguments for reconnecting after a connection loss
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
	client, err := dial(ctx, u.Host, username, password, hostKeyCallback, connLogger)
	if err != nil {
		return nil, err
	}
	return &fileSystem{
		client:              client,
		connLogger:          connLogger,
		prefix:              prefix,
		address:             address,
		credentialsCallback: credentialsCallback,
		hostKeyCallback:     hostKeyCallback,
	}, nil
}

func prepareDial(address string, credentialsCallback CredentialsCallback, hostKeyCallback ssh.HostKeyCallback) (u *url.URL, username, password, prefix string, err error) {
	if !strings.HasPrefix(address, "sftp://") {
		if strings.Contains(address, "://") {
			return nil, "", "", "", fmt.Errorf("not an SFTP URL scheme: %s", address)
		}
		address = "sftp://" + address
	}
	if credentialsCallback == nil {
		return nil, "", "", "", errors.New("nil credentialsCall")
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
	u, username, password, prefix, err := prepareDial(address, credentialsCallback, hostKeyCallback)
	if err != nil {
		return nop, err
	}
	f := fs.GetFileSystemByPrefixOrNil(prefix)
	if f != nil {
		fs.Register(f) // Increase ref count
		return func() error { fs.Unregister(f); return nil }, nil
	}

	client, err := dial(ctx, u.Host, username, password, hostKeyCallback, connLogger)
	if err != nil {
		return nop, err
	}
	f = &fileSystem{
		client:              client,
		connLogger:          connLogger,
		prefix:              prefix,
		address:             address,
		credentialsCallback: credentialsCallback,
		hostKeyCallback:     hostKeyCallback,
	}
	fs.Register(f) // TODO somone else might have registered, so free should not close it
	return func() error { return f.Close() }, nil
}

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
	if connLogger != nil {
		connLogger.Printf("Dialed SFTP connection to %s with user %s", host, user)
	}
	return sftp.NewClient(ssh.NewClient(sshConn, chans, reqs))
}

// func NewFileSystem(addr string, conn *ssh.Client) (*FileSystem, error) {
// 	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "sftp://"), "/")

// 	client, err := sftp.NewClient(conn)
// 	if err != nil {
// 		return nil, err
// 	}
// 	fileSystem := &FileSystem{
// 		client: client,
// 		prefix: "sftp://" + addr,
// 	}
// 	fs.Register(fileSystem)
// 	return fileSystem, nil
// }

func nop() error { return nil }

// isConnectionError returns true if the error indicates a connection loss
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "closed network connection") ||
		strings.Contains(errStr, "connection closed") ||
		strings.Contains(errStr, "connection lost") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection timed out") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "software caused connection abort") ||
		strings.Contains(errStr, "ssh_msg_disconnect") ||
		strings.Contains(errStr, "transport endpoint") ||
		strings.Contains(errStr, "use of closed")
}

// reconnectAndSetClient attempts to re-establish the SFTP connection using stored credentials.
// It uses prepareDial and dial to recreate the connection
// and stores the new connection in f.client while locking the f.clientMtx.
func (f *fileSystem) reconnectAndSetClient(ctx context.Context) error {
	// Check if we have the necessary connection parameters
	if f.address == "" || f.credentialsCallback == nil || f.hostKeyCallback == nil {
		return fmt.Errorf("cannot reconnect: missing connection parameters")
	}

	if f.connLogger != nil {
		f.connLogger.Printf("Reconnecting SFTP to %s", f.address)
	}

	// Use prepareDial to get connection details
	u, username, password, _, err := prepareDial(f.address, f.credentialsCallback, f.hostKeyCallback)
	if err != nil {
		return fmt.Errorf("reconnect prepareDial failed: %w", err)
	}

	f.clientMtx.Lock()
	defer f.clientMtx.Unlock()

	// Close existing broken connection if any
	if f.client != nil {
		_ = f.client.Close() // ignore error
		f.client = nil
	}

	// Establish new connection using dial
	f.client, err = dial(ctx, u.Host, username, password, f.hostKeyCallback, f.connLogger)
	if err != nil {
		return fmt.Errorf("reconnect dial failed: %w", err)
	}

	return nil
}

func (f *fileSystem) getClient(ctx context.Context, filePath string) (client *sftp.Client, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}

	// Progressive reconnection with exponential backoff
	backoff := InitialRetryBackoff
	maxRetries := MaxConnectRetries

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Apply backoff delay for retries
		if attempt > 0 {
			if f.connLogger != nil {
				f.connLogger.Printf("Waiting %s before retry attempt %d of %d of SFTP connection to %s", backoff, attempt, maxRetries, f.address)
			}
			select {
			case <-ctx.Done():
				return nil, "", nop, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff: 100ms, 200ms, 400ms
			}
		}

		f.clientMtx.RLock()
		client = f.client
		f.clientMtx.RUnlock()
		if client != nil {
			return client, filePath, nop, nil
		}

		// If we have connection parameters, try to reconnect
		if f.address != "" && f.credentialsCallback != nil && f.hostKeyCallback != nil {
			err = f.reconnectAndSetClient(ctx)
			if err == nil {
				return f.client, filePath, nop, nil
			}
			// On connection error during reconnect, retry
			if attempt < MaxConnectRetries {
				continue
			}
			return nil, "", nop, fmt.Errorf("failed to reconnect after %d attempts: %w", MaxConnectRetries, err)
		}

		// Fallback: try to dial with credentials from URL
		u, parseErr := url.Parse(f.URL(filePath))
		if parseErr != nil {
			return nil, "", nop, parseErr
		}
		username := u.User.Username()
		if username == "" {
			return nil, "", nop, fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
		}
		password, ok := u.User.Password()
		if !ok {
			return nil, "", nop, fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
		}
		client, err = dial(ctx, u.Host, username, password, AcceptAnyHostKey, f.connLogger)
		if err != nil {
			if attempt < MaxConnectRetries && isConnectionError(err) {
				continue
			}
			return nil, "", nop, err
		}
		return client, u.Path, func() error { return client.Close() }, nil
	}

	return nil, "", nop, fmt.Errorf("failed to get client after %d attempts", MaxConnectRetries)
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.prefix + Separator)
}

func (f *fileSystem) ID() (string, error) {
	return f.prefix, nil
}

func (f *fileSystem) Prefix() string {
	return f.prefix
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) Name() string {
	return "SFTP"
}

func (f *fileSystem) String() string {
	return f.prefix + " file system"
}

func (f *fileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *fileSystem) CleanPathFromURI(uri string) string {
	return path.Clean(
		strings.TrimPrefix(
			strings.TrimPrefix(uri, f.prefix),
			":22", // In case f.prefix has no port number and url has the default port number
		),
	)
}

func (*fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, Prefix, Separator)
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	path := f.JoinCleanPath(uriParts...)
	if strings.HasSuffix(f.prefix, Separator) && strings.HasPrefix(path, Separator) {
		// For example: "sftp://" + "/example.com/absolute/path"
		// should not result in 3 slashes: "sftp:///example.com/absolute/path"
		path = path[len(Separator):]
	}
	return fs.File(f.prefix + path)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.prefix, Separator)
}

func (f *fileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, Prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return Prefix + strings.TrimPrefix(filePath, Separator)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (f *fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (f *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	client, dirPath, release, err := f.getClient(context.Background(), dirPath)
	if err != nil {
		return err
	}
	defer release()

	return client.Mkdir(dirPath)
}

func (f *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	client, filePath, release, err := f.getClient(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	return client.Stat(filePath)
}

func (f *fileSystem) IsHidden(filePath string) bool       { return false }
func (f *fileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	client, dirPath, release, err := f.getClient(ctx, dirPath)
	if err != nil {
		return err
	}
	defer release()

	infos, err := client.ReadDirContext(ctx, dirPath)
	if err != nil {
		// Should we replace alls os.ErrNotExist errors with fs.ErrDoesNotExist?
		// if errors.Is(err, os.ErrNotExist) {
		// 	return fs.NewErrDoesNotExist(f.JoinCleanFile(dirPath))
		// }
		return err
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		match, err := fsimpl.MatchAnyPattern(info.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		err = callback(fs.NewFileInfo(f.JoinCleanFile(dirPath, info.Name()), info, false))
		if err != nil {
			return err
		}
	}
	return nil
}

type sftpFile struct {
	*sftp.File
	release func() error
}

func (f *sftpFile) Close() error {
	return errors.Join(f.File.Close(), f.release())
}

func (f *fileSystem) openFile(filePath string, flags int) (*sftpFile, error) {
	client, filePath, release, err := f.getClient(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	file, err := client.OpenFile(filePath, flags)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	return &sftpFile{file, release}, nil
}

func (f *fileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	return f.openFile(filePath, os.O_RDONLY)
}

func (f *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	return f.openFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}

func (f *fileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	file, err := f.openFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	// info, err := file.Stat()
	// if err != nil {
	// 	return nil, errors.Join(err, file.Close())
	// }
	// _, err = file.Seek(info.Size(), io.SeekStart)
	// if err != nil {
	// 	return nil, errors.Join(err, file.Close())
	// }
	return file, nil
}

func (f *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.openFile(filePath, os.O_RDWR|os.O_CREATE)
}

func (f *fileSystem) Truncate(filePath string, size int64) error {
	file, err := f.openFile(filePath, os.O_RDWR)
	if err != nil {
		return err
	}
	return errors.Join(
		file.Truncate(size),
		file.Close(),
	)
}

func (f *fileSystem) Move(filePath string, destPath string) error {
	client, filePath, release, err := f.getClient(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Rename(filePath, destPath)
}

func (f *fileSystem) Remove(filePath string) error {
	client, filePath, release, err := f.getClient(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Remove(filePath)
}

func (f *fileSystem) Close() error {
	if f.client == nil {
		return nil // already closed
	}
	count := fs.Unregister(f)
	if count > 1 {
		return nil // still referenced
	}
	err := f.client.Close()
	f.client = nil
	return err
}
