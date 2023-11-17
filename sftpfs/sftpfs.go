// Package sftpfs implements a SFTP client file system.
package sftpfs

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"net"
	"net/url"
	"os"
	"strings"

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
	fs.Register(new(fileSystem))
}

// LoginCallback is called by Dial to get the username and password for a SFTP connection.
type LoginCallback func(*url.URL) (username, password string, err error)

// Password returns a LoginCallback that always returns
// the provided password together with the username
// from the URL that is passed to the callback.
func Password(password string) LoginCallback {
	return func(u *url.URL) (string, string, error) {
		return u.User.String(), password, nil
	}
}

// UsernameAndPassword returns a LoginCallback that always returns
// the provided username and password.
func UsernameAndPassword(username, password string) LoginCallback {
	return func(u *url.URL) (string, string, error) {
		return username, password, nil
	}
}

// UsernameAndPasswordFromURL is a LoginCallback that returns
// the username and password encoded in the passed URL.
func UsernameAndPasswordFromURL(u *url.URL) (username, password string, err error) {
	password, ok := u.User.Password()
	if !ok {
		return "", "", fmt.Errorf("no password in URL: %s", u.String())
	}
	return u.User.Username(), password, nil
}

// AcceptAnyHostKey can be passed as hostKeyCallback to Dial
// to accept any SSH public key from a remote host.
func AcceptAnyHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return nil
}

type fileSystem struct {
	client *sftp.Client
	prefix string
}

// Dial dials a new SFTP connection without registering it as file system.
//
// The passed address can be a URL with scheme `sftp:` or just a host name.
// If no port is provided in the address, then port 22 will be used.
// The address can contain a username or a username and password.
func Dial(ctx context.Context, address string, loginCallback LoginCallback, hostKeyCallback ssh.HostKeyCallback) (fs.FileSystem, error) {
	if !strings.HasPrefix(address, "sftp://") {
		if strings.Contains(address, "://") {
			return nil, fmt.Errorf("URL must start with sftp:// but got %s", address)
		}
		address = "sftp://" + address
	}
	if loginCallback == nil {
		return nil, fmt.Errorf("missing loginCallback")
	}
	if hostKeyCallback == nil {
		return nil, fmt.Errorf("missing hostKeyCallback")
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "sftp" {
		return nil, fmt.Errorf("URL scheme must be sftp:// but got %s://", u.Scheme)
	}
	if u.Port() == "" {
		u.Host += ":22"
	}

	username, password, err := loginCallback(u)
	if err != nil {
		return nil, err
	}
	if username == "" {
		return nil, fmt.Errorf("missing SFTP username for: %s", address)
	}
	if password == "" {
		return nil, fmt.Errorf("missing SFTP password for: %s", address)
	}
	client, err := dial(ctx, u.Host, username, password, hostKeyCallback)
	if err != nil {
		return nil, err
	}
	return &fileSystem{
		client: client,
		prefix: fmt.Sprintf("sftp://%s@%s", url.User(username), u.Host),
	}, nil

}

// DialAndRegister dials a new SFTP connection and register it as file system.
//
// The passed address can be a URL with scheme `sftp:` or just a host name.
// If no port is provided in the address, then port 22 will be used.
// The address can contain a username or a username and password.
func DialAndRegister(ctx context.Context, address string, loginCallback LoginCallback, hostKeyCallback ssh.HostKeyCallback) (fs.FileSystem, error) {
	fileSystem, err := Dial(ctx, address, loginCallback, hostKeyCallback)
	if err != nil {
		return nil, err
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

func dial(ctx context.Context, host, user, password string, hostKeyCallback ssh.HostKeyCallback) (*sftp.Client, error) {
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: hostKeyCallback,
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, host, config)
	if err != nil {
		return nil, err
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

func (f *fileSystem) getClient(ctx context.Context, filePath string) (client *sftp.Client, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}
	if f.client != nil {
		return f.client, filePath, nop, nil
	}

	// Dial with credentials from URL to create client on the fly for caller:
	url, err := url.Parse(f.URL(filePath))
	if err != nil {
		return nil, "", nop, err
	}
	username := url.User.Username()
	if username == "" {
		return nil, "", nop, fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
	}
	password, ok := url.User.Password()
	if !ok {
		return nil, "", nop, fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
	}
	client, err = dial(ctx, url.Host, username, password, AcceptAnyHostKey)
	if err != nil {
		return nil, "", nop, err
	}
	return client, url.Path, func() error { return client.Close() }, nil
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
	if f.prefix == "" {
		return Prefix
	}
	return f.prefix
}

func (f *fileSystem) Name() string {
	return "SFTP"
}

func (f *fileSystem) String() string {
	return f.prefix + " file system"
}

func (*fileSystem) URL(cleanPath string) string {
	return Prefix + cleanPath
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

func (*fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, Prefix, Separator)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), Separator)
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, Prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return Prefix + strings.TrimPrefix(filePath, Separator)
}

func (f *fileSystem) SplitDirAndName(filePath string) (dir, name string) {
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

	infos, err := client.ReadDir(dirPath)
	if err != nil {
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
	return f.openFile(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC)
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
	fs.Unregister(f)
	f.client = nil
	return nil
}
